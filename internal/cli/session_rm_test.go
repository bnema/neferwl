package cli

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/sessiondir"
)

func TestSessionRemove(t *testing.T) {
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	d, err := sessiondir.Create(runtime, "remove")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	path := filepath.Join(d.Path(), "control.sock")
	if err := d.WriteMeta(sessiondir.Meta{ID: "remove", Control: path}); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	result := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			result <- err
			return
		}
		defer conn.Close()
		var req map[string]any
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			result <- err
			return
		}
		if req["action"] != "quit" || req["wait"] != "committed" {
			result <- os.ErrInvalid
			return
		}
		if err := json.NewEncoder(conn).Encode(map[string]any{"id": req["id"], "ok": true}); err != nil {
			result <- err
			return
		}
		result <- d.Close()
	}()
	code, _, stderr := execute(t, "session", "rm", "remove")
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if code != 0 || stderr != "" {
		t.Fatalf("%d %s", code, stderr)
	}
	if _, err := os.Stat(d.Path()); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

//testpolicy:realtime Blocking flock cancellation is tested against a real lifetime lock.
func TestSessionLockTimeout(t *testing.T) {
	runtime := t.TempDir()
	d, err := sessiondir.Create(runtime, "held")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(d.Path(), "lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := waitSessionLock(ctx, lock); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestTerminateSessionSafety(t *testing.T) {
	for _, pid := range []int{0, 1, os.Getpid()} {
		if err := terminateSession(sessiondir.Meta{PID: pid}); err == nil {
			t.Fatal(pid)
		}
	}
}
