package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/sessiondir"
)

func TestResolveControl(t *testing.T) {
	runtime := t.TempDir()
	if err := os.Mkdir(filepath.Join(runtime, "neferwl"), 0700); err != nil {
		t.Fatal(err)
	}
	derived := filepath.Join(runtime, "neferwl", "legacy.control.sock")
	if err := os.WriteFile(derived, nil, 0600); err != nil {
		t.Fatal(err)
	}
	sessions := []sessiondir.Meta{{ID: "b", Wayland: "wayland-b", Control: "/b"}, {ID: "a", Wayland: "wayland-a", Control: "/a"}}
	for _, tc := range []struct {
		name, flag string
		env        map[string]string
		want, err  string
	}{
		{"flag", "a", map[string]string{"NEFERWL_SESSION": "b", "NEFERWL_CONTROL": "/override", "WAYLAND_DISPLAY": "wayland-b"}, "/a", ""},
		{"session", "", map[string]string{"NEFERWL_SESSION": "b", "NEFERWL_CONTROL": "/override"}, "/b", ""},
		{"path", "", map[string]string{"NEFERWL_CONTROL": "/override", "WAYLAND_DISPLAY": "wayland-b"}, "/override", ""},
		{"display", "", map[string]string{"WAYLAND_DISPLAY": "wayland-a"}, "/a", ""},
		{"legacy", "", map[string]string{"XDG_RUNTIME_DIR": runtime, "WAYLAND_DISPLAY": "legacy"}, derived, ""},
		{"unknown", "missing", map[string]string{"NEFERWL_CONTROL": "/override"}, "", "live sessions: a, b"},
		{"invalid", "../a", nil, "", "invalid session id"},
		{"none", "", nil, "", "live sessions: a, b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveControl(tc.flag, func(k string) string { return tc.env[k] }, func() ([]sessiondir.Meta, error) { return sessions, nil })
			if got != tc.want || tc.err == "" && err != nil || tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
				t.Fatalf("%s %v", got, err)
			}
		})
	}
	sentinel := errors.New("list failed")
	_, err := resolveControl("a", func(string) string { return "" }, func() ([]sessiondir.Meta, error) { return nil, sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	got, err := resolveControl("", func(k string) string {
		if k == "NEFERWL_CONTROL" {
			return "/direct"
		}
		return ""
	}, func() ([]sessiondir.Meta, error) { t.Fatal("explicit path should not list"); return nil, nil })
	if got != "/direct" || err != nil {
		t.Fatal(got, err)
	}
}

func TestControlTargetFlag(t *testing.T) {
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("NEFERWL_SESSION", "")
	t.Setenv("NEFERWL_CONTROL", "/nonexistent-control")
	t.Setenv("WAYLAND_DISPLAY", "")
	d, err := sessiondir.Create(runtime, "chosen")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.WriteMeta(sessiondir.Meta{ID: "chosen", Control: filepath.Join(d.Path(), "selected.sock")}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-s", "chosen", "query", "state"}, {"query", "-s", "chosen", "state"}, {"do", "-s", "chosen", "none"}, {"state", "-s", "chosen"}} {
		code, _, stderr := execute(t, args...)
		if code != 1 || !strings.Contains(stderr, "selected.sock") {
			t.Fatalf("%v: %d %s", args, code, stderr)
		}
	}
}
