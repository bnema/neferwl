package xwayland

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"golang.org/x/sys/unix"
)

type builtinStarter struct{}

func (builtinStarter) start(ctx context.Context, d *Display, stderr *logWriter) error {
	// Files own all setup descriptors until Start succeeds and Attach takes
	// the Wayland socket. This also unwinds partial setup failures.
	var files []*os.File
	defer func() {
		for _, f := range files {
			// Files already closed on the success path report os.ErrClosed.
			if f != nil {
				if err := f.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
					d.opts.Log.Warn().Err(err).Str("name", f.Name()).Msg("close xwayland setup fd")
				}
			}
		}
	}()
	pair := func(name string) (int, error) {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			return 0, err
		}
		i := len(files)
		files = append(files, os.NewFile(uintptr(fds[0]), name+" parent"), os.NewFile(uintptr(fds[1]), name+" child"))
		return i, nil
	}
	wl, err := pair("wayland")
	if err != nil {
		return err
	}
	wm, err := pair("wm")
	if err != nil {
		return err
	}
	var pipe [2]int
	if err := unix.Pipe2(pipe[:], unix.O_CLOEXEC); err != nil {
		return err
	}
	rd := os.NewFile(uintptr(pipe[0]), "displayfd read")
	wr := os.NewFile(uintptr(pipe[1]), "displayfd write")
	files = append(files, rd, wr)
	args := []string{d.Name(), "-rootless"}
	for i := range d.sockets {
		args = append(args, "-listenfd", strconv.Itoa(3+i))
	}
	wlChild := 3 + len(d.sockets)
	args = append(args, "-wm", strconv.Itoa(wlChild+1), "-displayfd", strconv.Itoa(wlChild+2))
	cmd := exec.Command(d.opts.Binary, args...)
	for _, e := range d.opts.Env {
		if !strings.HasPrefix(e, "WAYLAND_DISPLAY=") && !strings.HasPrefix(e, "WAYLAND_SOCKET=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "WAYLAND_SOCKET="+strconv.Itoa(wlChild))
	cmd.ExtraFiles = append(append([]*os.File(nil), d.sockets...), files[wl+1], files[wm+1], wr)
	cmd.Stderr = stderr
	cmd.WaitDelay = 2 * time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	logging.CloseFile(d.opts.Log, files[wl+1], "xwayland child socket")
	logging.CloseFile(d.opts.Log, files[wm+1], "xwayland child wm socket")
	logging.CloseFile(d.opts.Log, wr, "xwayland displayfd write end")
	d.opts.Log.Info().Str("display", d.Name()).Int("pid", cmd.Process.Pid).Msg("Xwayland started")
	// Detach the Wayland descriptor from os.File ownership before handing it
	// to the display (including its finalizers).
	if d.opts.Attach != nil {
		// Dup transfers an independent descriptor; closing the File wrapper below
		// cannot race with the display's ownership of the transferred descriptor.
		var wlFD int
		wlFD, err = unix.FcntlInt(uintptr(files[wl].Fd()), unix.F_DUPFD_CLOEXEC, 0)
		if err == nil {
			d.opts.Attach(wlFD)
		}
	}
	logging.CloseFile(d.opts.Log, files[wl], "xwayland wayland socket")
	done := make(chan error, 1)
	procGone := make(chan struct{})
	go func() { done <- cmd.Wait(); close(procGone) }()
	if err != nil {
		stopGroup(cmd, done)
		return err
	}
	exited, readyErr := waitReady(ctx, pipe[0], done)
	if readyErr != nil {
		err := readyErr
		if errors.Is(err, errReadinessTimeout) {
			d.opts.Log.Warn().Str("display", d.Name()).Msg("Xwayland readiness timed out")
		}
		// Join Wait unless readiness already observed process exit.
		if !exited {
			stopGroup(cmd, done)
		}
		return err
	}
	logging.CloseFile(d.opts.Log, rd, "xwayland displayfd read end")
	d.opts.Log.Info().Str("display", d.Name()).Msg("Xwayland ready")
	if d.opts.AttachWM != nil {
		// The WM socket is bootstrapped only now: Xwayland serves it after its
		// Wayland round trips, which the display goroutine must be free to answer.
		wmFD, err := unix.FcntlInt(uintptr(files[wm].Fd()), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			stopGroup(cmd, done)
			return err
		}
		// The context ends with the process or the caller's context, so a
		// bootstrap against a dead or silent Xwayland aborts at once.
		wmCtx, cancel := context.WithCancel(ctx)
		watched := make(chan struct{})
		go func() {
			defer close(watched)
			select {
			case <-procGone:
				cancel()
			case <-wmCtx.Done():
			}
		}()
		stop := d.opts.AttachWM(wmCtx, wmFD)
		cancel()
		<-watched
		if stop != nil {
			// Runs before start returns so the restart loop never overlaps two XWMs.
			defer stop()
		}
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		stopGroup(cmd, done)
		return ctx.Err()
	}
}

var errReadinessTimeout = errors.New("xwayland readiness timed out")

// waitReady uses bounded poll waits rather than a pipe reader goroutine.
// The result reports whether cmd.Wait was already joined.
func waitReady(ctx context.Context, fd int, done <-chan error) (bool, error) {
	deadline := time.Now().Add(10 * time.Second)
	var buf [32]byte
	n := 0
	for {
		select {
		case err := <-done:
			if err == nil {
				err = errors.New("xwayland exited before readiness")
			}
			return true, err
		default:
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if time.Now().After(deadline) {
			return false, errReadinessTimeout
		}
		// A closed pipe also covers process exit, including exec's stderr delay.
		p := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		_, err := unix.Poll(p, 50)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, err
		}
		if p[0].Revents == 0 {
			continue
		}
		if p[0].Revents&unix.POLLIN != 0 {
			count, err := unix.Read(fd, buf[n:])
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil {
				return false, err
			}
			n += count
			if n > 0 && buf[n-1] == '\n' {
				if _, err := strconv.Atoi(strings.TrimSpace(string(buf[:n]))); err != nil {
					return false, fmt.Errorf("xwayland displayfd: %w", err)
				}
				return false, nil
			}
			if n == len(buf) {
				return false, errors.New("invalid Xwayland displayfd response")
			}
		}
		if p[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return false, errors.New("xwayland exited before readiness")
		}
	}
}

func stopGroup(cmd *exec.Cmd, done <-chan error) {
	_ = unix.Kill(-cmd.Process.Pid, unix.SIGTERM)
	select {
	case exitErr := <-done:
		_ = exitErr // exit status after our SIGTERM/SIGKILL is expected, not a failure
	case <-time.After(2 * time.Second):
		_ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
		exitErr := <-done
		_ = exitErr // exit status after our SIGTERM/SIGKILL is expected, not a failure
	}
}
