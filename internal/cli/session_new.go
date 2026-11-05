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
	readyCtx, cancel := context.WithTimeout(ctx, *startTimeout)
	defer cancel()
	ready, err := startSession(readyCtx, exe, sessionArgs(*id, *profile, *size, *config, *ttl, fs.Args()), sessionEnvironment(os.Environ(), *dbus))
	if err != nil {
		return err
	}
	if *shellEnv {
		_, err = fmt.Fprintf(env.Stdout, "NEFERWL_SESSION=%s\nWAYLAND_DISPLAY=%s\n", ready.ID, ready.Wayland)
		return err
	}
	return json.NewEncoder(env.Stdout).Encode(ready)
}

// startSession detaches the child but owns it until readiness or failure.
func startSession(ctx context.Context, exe string, args, environ []string) (sessionReady, error) {
	var ready sessionReady
	reader, writer, err := os.Pipe()
	if err != nil {
		return ready, err
	}
	defer reader.Close()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		writer.Close()
		return ready, err
	}
	defer null.Close()
	cmd := exec.Command(exe, args...)
	cmd.Env = environ
	cmd.Stdin = null
	cmd.Stdout = null
	cmd.Stderr = null
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.ExtraFiles = []*os.File{writer}
	if err := cmd.Start(); err != nil {
		writer.Close()
		return ready, err
	}
	writer.Close()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
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
		_ = cmd.Process.Kill()
		status := <-exited
		return ready, fmt.Errorf("session startup failed: %w (exit status: %v)", r.err, status)
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-exited
		return ready, fmt.Errorf("session startup: %w", ctx.Err())
	}
}
