package launcher

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/logging"
	"github.com/bnema/nefertty/internal/ports"
)

func TestChildEnv(t *testing.T) {
	got := ChildEnv([]string{"TERM=x", "DISPLAY=:0", "WAYLAND_SOCKET=4", "WAYLAND_DISPLAY=old", "HOME=/home/me", "PATH=/bin", "LC_ALL=C", "XDG_RUNTIME_DIR=old", "XCURSOR_THEME=Adwaita", "SECRET=bad"}, "wayland-7", "/run/me")
	want := []string{"HOME=/home/me", "LC_ALL=C", "PATH=/bin", "WAYLAND_DISPLAY=wayland-7", "XCURSOR_SIZE=24", "XCURSOR_THEME=Adwaita", "XDG_CURRENT_DESKTOP=nefertty", "XDG_RUNTIME_DIR=/run/me", "XDG_SESSION_TYPE=wayland"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "result")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	reqs := make(chan ports.SpawnRequest, 3)
	go func() {
		done <- New([]string{"PATH=/bin", "FILE=" + file}, logging.For(ctx, "launcher")).Run(ctx, reqs)
	}()
	reqs <- ports.SpawnRequest{}
	reqs <- ports.SpawnRequest{Argv: []string{"nonexistent-nefertty-binary"}}
	// Env adds to the child environment (slot tokens).
	reqs <- ports.SpawnRequest{Argv: []string{"sh", "-c", "echo ok$NEFERTTY_SLOT > $FILE"}, Env: []string{"NEFERTTY_SLOT=7"}}
	deadline := time.After(3 * time.Second)
	for {
		data, err := os.ReadFile(file)
		if err == nil {
			if string(data) != "ok7\n" {
				t.Fatalf("output: %q", data)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("child did not write")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("launcher did not stop")
	}
}

func TestChildPath(t *testing.T) {
	dir := t.TempDir()
	name := "nefertty-child-only-script"
	result := filepath.Join(dir, "result")
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nprintf ok > \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reqs := make(chan ports.SpawnRequest, 1)
	done := make(chan error, 1)
	go func() { done <- New([]string{"PATH=" + dir}, logging.For(ctx, "launcher")).Run(ctx, reqs) }()
	reqs <- ports.SpawnRequest{Argv: []string{name, result}}
	deadline := time.After(3 * time.Second)
	for {
		if data, err := os.ReadFile(result); err == nil {
			if string(data) != "ok" {
				t.Fatalf("result: %q", data)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("child not started")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("launcher hung")
	}
}
