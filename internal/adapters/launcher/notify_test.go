package launcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"golang.org/x/sys/unix"
)

// notify-send runs with the child environment and the summary and body as
// separate arguments; the script on PATH records them.
//
//testpolicy:realtime notify-send is a real subprocess writing to a FIFO
func TestNotifier(t *testing.T) {
	dir := t.TempDir()
	// The script writes to a FIFO the test reads: the open blocks until both
	// ends exist and the read ends when the script exits.
	out := filepath.Join(dir, "calls")
	if err := unix.Mkfifo(out, 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s|' \"$@\" \"$DBUS_SESSION_BUS_ADDRESS\" > " + out + "\n"
	if err := os.WriteFile(filepath.Join(dir, "notify-send"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	log := logging.For(ctx, "launcher")
	NewNotifier(ctx, []string{"PATH=" + dir + ":/usr/bin:/bin", "DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/bus"}, log).Notify("DP-2 scale saved", "-Scale 1.5")
	// Notify returns at once; the FIFO read ends when the script exits. One
	// deadline bounds it; closing a write end releases a reader left behind.
	type result struct {
		data []byte
		err  error
	}
	got := make(chan result, 1)
	go func() {
		data, err := os.ReadFile(out)
		got <- result{data, err}
	}()
	var data []byte
	select {
	case r := <-got:
		if r.err != nil {
			t.Fatal(r.err)
		}
		data = r.data
	case <-time.After(5 * time.Second):
		if f, err := os.OpenFile(out, os.O_WRONLY|unix.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
		t.Fatal("notify-send not run")
	}
	if want := "--app-name=neferwl|--|DP-2 scale saved|-Scale 1.5|unix:path=/tmp/bus|"; string(data) != want {
		t.Fatalf("got %q, want %q", data, want)
	}
	// A missing notify-send is skipped, not fatal.
	NewNotifier(ctx, []string{"PATH=" + t.TempDir()}, log).Notify("x", "y")
}
