package launcher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bnema/nefertty/internal/logging"
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
		"systemctl --user unset-environment WAYLAND_DISPLAY DISPLAY XDG_CURRENT_DESKTOP XDG_SESSION_TYPE wayland-9\n"
	if string(data) != want {
		t.Fatalf("calls:\n%s", data)
	}
	// Missing tools are skipped, not fatal.
	ExportSession(ctx, []string{"PATH=" + t.TempDir()}, logging.For(ctx, "launcher"))
	if strings.Count(string(data), "\n") != 2 {
		t.Fatal(string(data))
	}
}
