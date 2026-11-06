package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/bnema/neferwl/internal/adapters/sessiondir"
)

func sessionRemove(ctx context.Context, env Env, args []string) (err error) {
	fs := flag.NewFlagSet("session rm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	timeout := fs.Duration("timeout", 30*time.Second, "Shutdown timeout.")
	if help, err := parseSubFlags(env, fs, "rm [flags] <id>", "Ask a live session to quit and wait for it to exit.", args); help || err != nil {
		return err
	}
	if len(fs.Args()) != 1 || !sessiondir.ValidID(fs.Args()[0]) {
		return usagef("expected a valid session ID")
	}
	if *timeout <= 0 {
		return usagef("timeout must be positive")
	}
	id := fs.Args()[0]
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	sessions, err := sessiondir.List(runtime)
	if err != nil {
		return err
	}
	var meta *sessiondir.Meta
	for i := range sessions {
		if sessions[i].ID == id {
			meta = &sessions[i]
			break
		}
	}
	if meta == nil {
		return fmt.Errorf("session %q is not live", id)
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	// Open before requesting quit: the owner may remove its directory on exit.
	lock, err := os.OpenFile(filepath.Join(sessiondir.Root(runtime), id, "lock"), os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	var connCloseErr error // reported after the wait and cleanup, never early
	c, err := dialPath(meta.Control)
	if err == nil {
		reply, callErr := c.call(ctx, map[string]any{"op": "do", "action": "quit", "wait": "committed"})
		connCloseErr = c.conn.Close()
		if callErr != nil && !errors.Is(callErr, io.EOF) {
			return errors.Join(callErr, connCloseErr)
		}
		if callErr == nil {
			if err := reply.err(); err != nil {
				return fmt.Errorf("session quit: %w", err)
			}
		}
	} else {
		if err := terminateSession(*meta); err != nil {
			return err
		}
	}
	if err := waitSessionLock(ctx, lock); err != nil {
		return errors.Join(err, connCloseErr)
	}
	// List performs stale cleanup under the registry lock, without removing a
	// new live session that may have reused the same ID during shutdown.
	_, err = sessiondir.List(runtime)
	return errors.Join(err, connCloseErr)
}

func terminateSession(meta sessiondir.Meta) error {
	if meta.PID <= 1 || meta.PID == os.Getpid() {
		return errors.New("refusing unsafe session PID")
	}
	ours, err := os.Executable()
	if err != nil {
		return err
	}
	oursInfo, err := os.Stat(ours)
	if err != nil {
		return err
	}
	proc := filepath.Join("/proc", strconv.Itoa(meta.PID), "exe")
	theirs, err := os.Stat(proc)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(oursInfo, theirs) {
		return fmt.Errorf("session PID %d is not this executable", meta.PID)
	}
	return syscall.Kill(meta.PID, syscall.SIGTERM)
}

// waitSessionLock blocks in flock rather than polling. On cancellation the
// worker retains its duplicate fd until the owner releases the lifetime lock.
func waitSessionLock(ctx context.Context, lock *os.File) error {
	fd, err := syscall.Dup(int(lock.Fd()))
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		err := syscall.Flock(fd, syscall.LOCK_EX)
		if err == nil {
			err = syscall.Flock(fd, syscall.LOCK_UN)
		}
		done <- errors.Join(err, syscall.Close(fd))
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
