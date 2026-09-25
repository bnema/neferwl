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
	"strconv"
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
// (xwayland-satellite 0.7 and later).
func Supported(binary string) bool {
	cmd := exec.Command(binary, ":0", "--test-listenfd-support")
	cmd.Env = []string{}
	return cmd.Run() == nil
}

// Open reserves the first free display number and listens on its sockets.
func Open(opts Options) (*Display, error) {
	if err := ensureDir(opts.Dir, opts.TmpDir); err != nil {
		return nil, err
	}
	for n := 0; n < 50; n++ {
		lock := filepath.Join(opts.TmpDir, fmt.Sprintf(".X%d-lock", n))
		fd, err := unix.Open(lock, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC, 0o444)
		if err != nil {
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
			d.Close()
			continue
		}
		return d, nil
	}
	return nil, errors.New("no free X11 display")
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
	for {
		if err := d.waitClient(ctx); err != nil {
			return nil
		}
		start := time.Now()
		err := d.serve(ctx)
		if ctx.Err() != nil {
			return nil
		}
		d.opts.Log.Info().Err(err).Str("display", d.Name()).Msg("xwayland-satellite exited")
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

// waitClient blocks until a socket has a pending connection.
func (d *Display) waitClient(ctx context.Context) error {
	fds := make([]unix.PollFd, len(d.sockets))
	for i, f := range d.sockets {
		fds[i] = unix.PollFd{Fd: int32(f.Fd()), Events: unix.POLLIN}
	}
	for ctx.Err() == nil {
		n, err := unix.Poll(fds, 200)
		if err != nil && !errors.Is(err, unix.EINTR) {
			return err
		}
		if n > 0 {
			return nil
		}
	}
	return ctx.Err()
}

// serve runs the satellite on the sockets until it exits or ctx ends.
func (d *Display) serve(ctx context.Context) error {
	args := []string{d.Name()}
	for i := range d.sockets {
		args = append(args, "-listenfd", strconv.Itoa(3+i))
	}
	cmd := exec.Command(d.opts.Binary, args...)
	cmd.Env = d.opts.Env
	cmd.ExtraFiles = d.sockets
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
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
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
