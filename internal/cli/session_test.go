package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/sessiondir"
)

func TestSessionList(t *testing.T) {
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	code, out, errOut := execute(t, "session", "ls", "--json")
	if code != 0 || strings.TrimSpace(out) != "[]" || errOut != "" {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	for _, id := range []string{"b", "a"} {
		d, err := sessiondir.Create(runtime, id)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		if err := d.WriteMeta(sessiondir.Meta{ID: id, Profile: "persistent", PID: 123, Wayland: "wayland-" + id, Created: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)}); err != nil {
			t.Fatal(err)
		}
	}
	code, out, errOut = execute(t, "session", "ls")
	if code != 0 || errOut != "" {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	for _, want := range []string{"ID", "PROFILE", "PID", "WAYLAND", "CREATED", "persistent", "wayland-a", "2026-11-05T09:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s: %s", want, out)
		}
	}
	code, out, errOut = execute(t, "session", "ls", "--json")
	var metas []sessiondir.Meta
	if err := json.Unmarshal([]byte(out), &metas); err != nil {
		t.Fatal(err)
	}
	if code != 0 || errOut != "" || len(metas) != 2 || metas[0].ID != "a" || metas[1].ID != "b" {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
}

func TestSessionListUsage(t *testing.T) {
	for _, args := range [][]string{{"session"}, {"session", "unknown"}, {"session", "ls", "extra"}, {"session", "ls", "--unknown"}} {
		code, _, stderr := execute(t, args...)
		if code != 2 || stderr == "" {
			t.Fatalf("%v: %d %s", args, code, stderr)
		}
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	code, _, stderr := execute(t, "session", "ls")
	if code != 1 || !strings.Contains(stderr, "XDG_RUNTIME_DIR") {
		t.Fatalf("%d %s", code, stderr)
	}
}
