package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bnema/neferwl/internal/adapters/sessiondir"
	"github.com/bnema/neferwl/internal/domain"
)

type sessionReady struct {
	ID      string `json:"id"`
	Wayland string `json:"wayland"`
	Control string `json:"control"`
	Display string `json:"display"`
}

func sessionEnvironment(environ []string, dbus bool) []string {
	var out []string
	for _, v := range environ {
		key, _, _ := strings.Cut(v, "=")
		switch key {
		case "NEFERWL_STATE", "NEFERWL_SLOT", "NEFERWL_CONTROL", "NEFERWL_SESSION", "WAYLAND_DISPLAY", "WAYLAND_SOCKET", "DISPLAY":
			continue
		case "DBUS_SESSION_BUS_ADDRESS":
			if !dbus {
				continue
			}
		}
		out = append(out, v)
	}
	return out
}
func sessionArgs(id, profile, size, config string, ttl time.Duration, extra []string) []string {
	args := []string{"--backend=headless", "--profile=" + profile, "--session-id=" + id, "--ready-fd=3"}
	if size != "" {
		args = append(args, "--size="+size)
	}
	if config != "" {
		args = append(args, "--config="+config)
	}
	if ttl > 0 {
		args = append(args, "--timeout="+ttl.String())
	}
	return append(args, extra...)
}

func sessionNew(ctx context.Context, env Env, args []string) error {
	fs := flag.NewFlagSet("session new", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	id := fs.String("id", "", "Session ID.")
	profile := fs.String("profile", "ephemeral", "ephemeral or persistent.")
	size := fs.String("size", "", "Output sizes.")
	config := fs.String("config", "", "Config path.")
	ttl := fs.Duration("ttl", 0, "Session lifetime.")
	startTimeout := fs.Duration("start-timeout", 30*time.Second, "Startup timeout.")
	dbus := fs.Bool("dbus-session", false, "Keep the parent session bus.")
	shellEnv := fs.Bool("env", false, "Print environment assignments instead of JSON.")
	if help, err := parseSubFlags(env, fs, "new [flags] [-- compositor flags]", "Start a detached headless session and print its readiness.", args); help || err != nil {
		return err
	}
	if *id == "" {
		*id = sessiondir.NewID()
	}
	if !sessiondir.ValidID(*id) {
		return usagef("invalid session id %q", *id)
	}
	if _, err := domain.ParseProfile(*profile); err != nil {
		return usagef("%v", err)
	}
	if *size != "" {
		if _, err := parseSizes(*size); err != nil {
			return err
		}
	}
	if *ttl < 0 || *startTimeout <= 0 {
		return usagef("ttl must be nonnegative and start-timeout positive")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	ready, err := startSessionInterruptible(ctx, *startTimeout, exe, sessionArgs(*id, *profile, *size, *config, *ttl, fs.Args()), sessionEnvironment(os.Environ(), *dbus))
	if err != nil {
		return err
	}
	if *shellEnv {
		_, err = fmt.Fprintf(env.Stdout, "NEFERWL_SESSION=%s\nWAYLAND_DISPLAY=%s\n", ready.ID, ready.Wayland)
		return err
	}
	return json.NewEncoder(env.Stdout).Encode(ready)
}

// startSessionInterruptible bounds startup by timeout and lets SIGINT or
// SIGTERM cancel it, so an interrupted CLI kills and reaps the detached child.
func startSessionInterruptible(ctx context.Context, timeout time.Duration, exe string, args, environ []string) (sessionReady, error) {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return startSession(ctx, exe, args, environ)
}

// closeStartFile closes one of startSession's files. After a successful start
// a close failure must not turn the ready session into an error and orphan the
// detached child, so it is only printed to stderr; on failure it is joined.
func closeStartFile(f *os.File, what string, err *error) {
	cerr := f.Close()
	switch {
	case cerr == nil:
	case *err == nil:
		fmt.Fprintf(os.Stderr, "neferwl: warning: close %s: %v\n", what, cerr)
	default:
		*err = errors.Join(*err, cerr)
	}
}

// startSession detaches the child but owns it until readiness or failure.
func startSession(ctx context.Context, exe string, args, environ []string) (ready sessionReady, err error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return ready, err
	}
	defer closeStartFile(reader, "readiness pipe", &err)
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return ready, errors.Join(err, writer.Close())
	}
	defer closeStartFile(null, os.DevNull, &err)
	cmd := exec.Command(exe, args...)
	cmd.Env = environ
	cmd.Stdin = null
	cmd.Stdout = null
	cmd.Stderr = null
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.ExtraFiles = []*os.File{writer}
	if err := cmd.Start(); err != nil {
		return ready, errors.Join(err, writer.Close())
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	if err := writer.Close(); err != nil {
		_ = cmd.Process.Kill() // best effort: the close error is what we report
		exitErr := <-exited
		_ = exitErr // exit status is expected after the kill
		return ready, err
	}
	type result struct {
		ready sessionReady
		err   error
	}
	decoded := make(chan result, 1)
	go func() {
		var r sessionReady
		line, err := bufio.NewReader(io.LimitReader(reader, 65537)).ReadBytes('\n')
		if err == nil {
			err = json.Unmarshal(line, &r)
		}
		if err == nil && (r.ID == "" || r.Wayland == "" || r.Control == "") {
			err = errors.New("incomplete session readiness reply")
		}
		decoded <- result{r, err}
	}()
	select {
	case r := <-decoded:
		if r.err == nil {
			return r.ready, nil
		}
		_ = cmd.Process.Kill() // the child may have exited already; its status is reported below
		status := <-exited
		return ready, fmt.Errorf("session startup failed: %w (exit status: %v)", r.err, status)
	case <-ctx.Done():
		_ = cmd.Process.Kill() // the child may have exited already
		exitErr := <-exited
		_ = exitErr // expected after killing the child
		return ready, fmt.Errorf("session startup: %w", ctx.Err())
	}
}
