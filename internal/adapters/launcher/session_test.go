package launcher

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/logging"
)

// The session commands run with the child environment and the right
// arguments; the scripts on PATH record them.
func TestExportSession(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"${0##*/} $* $WAYLAND_DISPLAY\" >> " + out + "\n"
	for _, name := range []string{"dbus-update-activation-environment", "systemctl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"PATH=" + dir, "WAYLAND_DISPLAY=wayland-9"}
	ctx := context.Background()
	ExportSession(ctx, env, logging.For(ctx, "launcher"))
	UnexportSession(env, logging.For(ctx, "launcher"))
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := "dbus-update-activation-environment --systemd WAYLAND_DISPLAY DISPLAY XDG_CURRENT_DESKTOP XDG_SESSION_TYPE wayland-9\n" +
		"systemctl --user --no-block try-restart xdg-desktop-portal*.service wayland-9\n" +
		"systemctl --user unset-environment WAYLAND_DISPLAY DISPLAY XDG_CURRENT_DESKTOP XDG_SESSION_TYPE wayland-9\n" +
		"systemctl --user --no-block stop xdg-desktop-portal*.service wayland-9\n"
	if string(data) != want {
		t.Fatalf("calls:\n%s", data)
	}
	// Missing tools are skipped, not fatal.
	ExportSession(ctx, []string{"PATH=" + t.TempDir()}, logging.For(ctx, "launcher"))
	if strings.Count(string(data), "\n") != 4 {
		t.Fatal(string(data))
	}
}

// A failed export restarts no portal: it would read the old variables again.
func TestExportSessionFailedSkipsPortals(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "calls")
	scripts := map[string]string{
		"dbus-update-activation-environment": "#!/bin/sh\nexit 1\n",
		"systemctl":                          "#!/bin/sh\necho \"$*\" >> " + out + "\n",
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	ExportSession(ctx, []string{"PATH=" + dir}, logging.For(ctx, "launcher"))
	if data, err := os.ReadFile(out); err == nil {
		t.Fatalf("systemctl called: %s", data)
	}
}

func TestNotifyReady(t *testing.T) {
	ctx := context.Background()
	log := logging.For(ctx, "launcher")
	NotifyReady("", log)
	path := filepath.Join(t.TempDir(), "notify")
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	NotifyReady(path, log)
	if err := listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, _, err := listener.ReadFromUnix(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "READY=1" {
		t.Fatalf("notification: %q", buf[:n])
	}
}
