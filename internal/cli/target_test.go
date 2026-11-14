package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/sessiondir"
)

func TestResolveControl(t *testing.T) {
	runtime := t.TempDir()
	// A <display>.control.sock beside the sessions is not a target.
	if err := os.Mkdir(filepath.Join(runtime, "neferwl"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtime, "neferwl", "legacy.control.sock"), nil, 0600); err != nil {
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
		{"legacy", "", map[string]string{"XDG_RUNTIME_DIR": runtime, "WAYLAND_DISPLAY": "legacy"}, "", "no control target"},
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
	defer func() { _ = d.Close() }()
	if err := d.WriteMeta(sessiondir.Meta{ID: "chosen", Control: filepath.Join(d.Path(), "selected.sock")}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-s", "chosen", "query", "state"}, {"query", "-s", "chosen", "state"}, {"do", "-s", "chosen", "none"}, {"do", "focus-workspace", "2", "-s", "chosen"}, {"do", "focus-workspace", "2", "-s=chosen"}, {"key", "Super+q", "-s", "chosen"}, {"state", "-s", "chosen"}} {
		code, _, stderr := execute(t, args...)
		if code != 1 || !strings.Contains(stderr, "selected.sock") {
			t.Fatalf("%v: %d %s", args, code, stderr)
		}
	}
}

func TestStateOutputOfTarget(t *testing.T) {
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("WAYLAND_DISPLAY", "live")
	d, err := sessiondir.Create(runtime, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	targetPath := filepath.Join(d.Path(), "state.json")
	livePath := filepath.Join(runtime, "neferwl", "live.json")
	for path, output := range map[string]string{targetPath: "TARGET", livePath: "LIVE"} {
		data := `{"outputs":[{"name":"` + output + `"}],"windows":[{"id":1,"pid":` + strconv.Itoa(os.Getpid()) + `,"output":"` + output + `"}]}`
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.WriteMeta(sessiondir.Meta{ID: "test", State: targetPath}); err != nil {
		t.Fatal(err)
	}
	for _, override := range []string{"", livePath} {
		t.Run("override="+override, func(t *testing.T) {
			t.Setenv("NEFERWL_STATE", override)
			for _, args := range [][]string{{"-s", "test", "state", "output-of", strconv.Itoa(os.Getpid())}, {"state", "-s", "test", "output-of", strconv.Itoa(os.Getpid())}} {
				code, out, stderr := execute(t, args...)
				if code != 0 || stderr != "" || !strings.Contains(out, `"name":"TARGET"`) || strings.Contains(out, "LIVE") {
					t.Fatalf("%v: %d %s %s", args, code, out, stderr)
				}
			}
		})
	}
	code, _, stderr := execute(t, "-s", "missing", "state", "output-of", strconv.Itoa(os.Getpid()))
	if code != 1 || !strings.Contains(stderr, "has no state file") {
		t.Fatalf("%d %s", code, stderr)
	}
}
