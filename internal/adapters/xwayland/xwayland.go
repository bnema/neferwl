// Package xwayland runs X11 clients through xwayland-satellite, started on
// demand: nefertty owns the X11 display sockets and starts the satellite
// when the first X11 client connects, and again after it exits.
package xwayland

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// Options configures the X11 display.
type Options struct {
	// Binary is the xwayland-satellite executable.
	Binary string
	// Env is the satellite's environment; it must hold WAYLAND_DISPLAY.
	Env []string
	// Dir holds the X11 sockets and TmpDir the lock files: /tmp/.X11-unix
	// and /tmp outside tests.
	Dir, TmpDir string
	// Abstract also listens on the abstract socket, as X servers do.
	Abstract bool
	Log      zerowrap.Logger
}

// Display is an X11 display served by xwayland-satellite.
type Display struct {
	opts    Options
	n       int
	sockets []*os.File // unix socket, then the abstract one
	socket  string
	lock    string
}

// Supported reports whether the binary can take over listening sockets
// (xwayland-satellite 0.7 and later). A binary that hangs is unsupported.
func Supported(ctx context.Context, binary string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, ":0", "--test-listenfd-support")
	cmd.Env = []string{}
	return cmd.Run() == nil
}

// Open reserves the first free display number and listens on its sockets.
func Open(opts Options) (*Display, error) {
	if err := ensureDir(opts.Dir, opts.TmpDir); err != nil {
		return nil, err
	}
	var last error
	for n := 0; n < 50; n++ {
		lock := filepath.Join(opts.TmpDir, fmt.Sprintf(".X%d-lock", n))
		fd, err := unix.Open(lock, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC, 0o444)
		if errors.Is(err, unix.EEXIST) && staleLock(lock) {
			// A crashed session left it: take the display over.
			opts.Log.Info().Str("lock", lock).Msg("removing stale X11 lock")
			_ = os.Remove(lock)
			fd, err = unix.Open(lock, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC, 0o444)
		}
		if err != nil {
			if !errors.Is(err, unix.EEXIST) {
				last = fmt.Errorf("create %s: %w", lock, err)
			}
			continue
		}
		_, err = unix.Write(fd, fmt.Appendf(nil, "%10d\n", os.Getpid()))
		unix.Close(fd)
		if err != nil {
			_ = os.Remove(lock)
			return nil, fmt.Errorf("write X11 lock: %w", err)
		}
		d := &Display{opts: opts, n: n, lock: lock, socket: filepath.Join(opts.Dir, fmt.Sprintf("X%d", n))}
		if err := d.listen(); err != nil {
			last = err
			d.Close()
			continue
		}
		return d, nil
	}
	if last != nil {
		return nil, fmt.Errorf("no free X11 display: %w", last)
	}
	return nil, errors.New("no free X11 display")
}

// staleLock reports whether a lock file names a process that is gone.
func staleLock(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return false
	}
	return errors.Is(unix.Kill(pid, 0), unix.ESRCH)
}

// ensureDir creates the X11 socket directory, or checks an existing one is
// shared and sticky like /tmp.
func ensureDir(dir, tmp string) error {
	err := unix.Mkdir(dir, 0o1777)
	if err == nil {
		// The umask may have dropped bits.
		return unix.Chmod(dir, 0o1777)
	}
	if !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	var st, tst unix.Stat_t
	if err := unix.Lstat(dir, &st); err != nil {
		return err
	}
	if err := unix.Lstat(tmp, &tst); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if st.Uid != tst.Uid && st.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("%s has the wrong owner", dir)
	}
	if st.Mode&0o022 != 0o022 || st.Mode&0o1000 == 0 {
		return fmt.Errorf("%s must be world-writable and sticky", dir)
	}
	return nil
}

func (d *Display) listen() error {
	names := []string{d.socket}
	if d.opts.Abstract {
		names = append(names, "@"+d.socket)
	}
	for _, name := range names {
		if name == d.socket {
			_ = os.Remove(name) // a stale socket without a lock
		}
		fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			return err
		}
		f := os.NewFile(uintptr(fd), name)
		addr := name
		if addr[0] == '@' {
			addr = "\x00" + addr[1:]
		}
		if err := unix.Bind(fd, &unix.SockaddrUnix{Name: addr}); err != nil {
			f.Close()
			return fmt.Errorf("bind %s: %w", name, err)
		}
		d.sockets = append(d.sockets, f)
		if err := unix.Listen(fd, 128); err != nil {
			return fmt.Errorf("listen %s: %w", name, err)
		}
	}
	return nil
}

// Name is the DISPLAY value, such as ":1".
func (d *Display) Name() string { return ":" + strconv.Itoa(d.n) }

// Close stops listening and removes the socket and lock files.
func (d *Display) Close() {
	for _, f := range d.sockets {
		f.Close()
	}
	if len(d.sockets) > 0 {
		_ = os.Remove(d.socket)
	}
	d.sockets = nil
	_ = os.Remove(d.lock)
}

// Run starts the satellite whenever an X11 client connects while it is not
// running, until ctx ends. The satellite takes over the sockets and accepts
// the waiting clients itself.
func (d *Display) Run(ctx context.Context) error {
	// The eventfd wakes waitClient when ctx ends; the waker is joined
	// before the fd closes.
	wake, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		return err
	}
	stopWaker := make(chan struct{})
	var waker sync.WaitGroup
	waker.Go(func() {
		select {
		case <-ctx.Done():
			_, _ = unix.Write(wake, []byte{1, 0, 0, 0, 0, 0, 0, 0})
		case <-stopWaker:
		}
	})
	defer func() { close(stopWaker); waker.Wait(); unix.Close(wake) }()
	for {
		if err := d.waitClient(ctx, wake); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			d.opts.Log.Warn().Err(err).Str("display", d.Name()).Msg("X11 display stopped")
			return err
		}
		start := time.Now()
		stderr := &logWriter{log: d.opts.Log}
		err := d.serve(ctx, stderr)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			d.opts.Log.Warn().Err(err).Strs("stderr", stderr.tail()).Str("display", d.Name()).Msg("xwayland-satellite exited")
		} else {
			d.opts.Log.Info().Str("display", d.Name()).Msg("xwayland-satellite exited")
		}
		// Clients the satellite never accepted would wake us at once: a
		// satellite that cannot start must not respawn in a loop.
		d.dropPending()
		if time.Since(start) < time.Second {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
		}
	}
}

// waitClient blocks until a socket has a pending connection or ctx ends.
func (d *Display) waitClient(ctx context.Context, wake int) error {
	fds := []unix.PollFd{{Fd: int32(wake), Events: unix.POLLIN}}
	for _, f := range d.sockets {
		fds = append(fds, unix.PollFd{Fd: int32(f.Fd()), Events: unix.POLLIN})
	}
	for ctx.Err() == nil {
		_, err := unix.Poll(fds, -1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("poll X11 sockets: %w", err)
		}
		for _, p := range fds[1:] {
			if p.Revents&(unix.POLLERR|unix.POLLNVAL|unix.POLLHUP) != 0 {
				return fmt.Errorf("X11 socket error (revents %#x)", p.Revents)
			}
			if p.Revents&unix.POLLIN != 0 {
				return nil
			}
		}
	}
	return ctx.Err()
}

// serve runs the satellite on the sockets until it exits or ctx ends.
func (d *Display) serve(ctx context.Context, stderr *logWriter) error {
	args := []string{d.Name()}
	for i := range d.sockets {
		args = append(args, "-listenfd", strconv.Itoa(3+i))
	}
	cmd := exec.Command(d.opts.Binary, args...)
	cmd.Env = d.opts.Env
	cmd.ExtraFiles = d.sockets
	cmd.Stderr = stderr
	// Xwayland inherits the stderr pipe: Wait must not wait for it.
	cmd.WaitDelay = 2 * time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	d.opts.Log.Info().Str("display", d.Name()).Int("pid", cmd.Process.Pid).Msg("xwayland-satellite started")
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// The satellite leads its session's process group, Xwayland included.
		pgid := -cmd.Process.Pid
		_ = unix.Kill(pgid, unix.SIGTERM)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = unix.Kill(pgid, unix.SIGKILL)
			<-done
		}
		return ctx.Err()
	}
}

// dropPending accepts and closes the connections waiting on the sockets.
func (d *Display) dropPending() {
	for _, f := range d.sockets {
		fd := int(f.Fd())
		if unix.SetNonblock(fd, true) != nil {
			continue
		}
		for {
			nfd, _, err := unix.Accept4(fd, unix.SOCK_CLOEXEC)
			if err != nil {
				break
			}
			unix.Close(nfd)
		}
		_ = unix.SetNonblock(fd, false)
	}
}

// logWriter logs the satellite's stderr line by line and keeps the last
// lines for the exit log; exec copies it from a pipe on its own goroutine.
type logWriter struct {
	mu   sync.Mutex
	log  zerowrap.Logger
	line []byte
	last []string
}

// tail is the last lines written.
func (w *logWriter) tail() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.last)
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.line = append(w.line, p...)
	for {
		i := strings.IndexByte(string(w.line), '\n')
		if i < 0 {
			break
		}
		if i > 0 {
			line := string(w.line[:i])
			w.log.Debug().Str("line", line).Msg("xwayland-satellite")
			if w.last = append(w.last, line); len(w.last) > 5 {
				w.last = w.last[1:]
			}
		}
		w.line = w.line[i+1:]
	}
	if len(w.line) > 4096 {
		w.line = w.line[:0]
	}
	return len(p), nil
}
