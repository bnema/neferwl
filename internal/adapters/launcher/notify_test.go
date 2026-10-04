package launcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
)

// notify-send runs with the child environment and the summary and body as
// separate arguments; the script on PATH records them.
func TestNotifier(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s|' \"$@\" \"$DBUS_SESSION_BUS_ADDRESS\" > " + out + ".tmp && mv " + out + ".tmp " + out + "\n"
	if err := os.WriteFile(filepath.Join(dir, "notify-send"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	log := logging.For(ctx, "launcher")
	NewNotifier(ctx, []string{"PATH=" + dir + ":/usr/bin:/bin", "DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/bus"}, log).Notify("DP-2 scale saved", "-Scale 1.5")
	// Notify returns at once; wait for the script to record the call.
	data, err := os.ReadFile(out)
	for deadline := time.Now().Add(5 * time.Second); err != nil; data, err = os.ReadFile(out) {
		if time.Now().After(deadline) {
			t.Fatal("notify-send not run")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if want := "--app-name=neferwl|--|DP-2 scale saved|-Scale 1.5|unix:path=/tmp/bus|"; string(data) != want {
		t.Fatalf("got %q, want %q", data, want)
	}
	// A missing notify-send is skipped, not fatal.
	NewNotifier(ctx, []string{"PATH=" + t.TempDir()}, log).Notify("x", "y")
}
