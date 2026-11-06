package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestControlCommands(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		want   map[string]any
		reject bool
	}{
		{"do", []string{"do", "focus-workspace", "2", "--wait", "committed", "--until", "window-mapped app_id=foot", "--timeout", "3s"}, map[string]any{"op": "do", "action": "focus-workspace 2", "wait": "committed", "until": "window-mapped app_id=foot", "timeout": "3s", "shot": nil}, false},
		{"do-shot", []string{"do", "focus-workspace", "2", "--shot"}, map[string]any{"op": "do", "action": "focus-workspace 2", "shot": true}, false},
		{"key", []string{"key", "Super+Return", "--wait", "committed", "--until", "window-mapped app_id=foot", "--timeout", "3s"}, map[string]any{"op": "inject", "kind": "key", "combo": "Super+Return", "wait": "committed", "until": "window-mapped app_id=foot", "timeout": "3s"}, false},
		{"key-shot", []string{"key", "Super+Return", "--shot=true"}, map[string]any{"op": "inject", "kind": "key", "shot": true}, false},
		{"type", []string{"type", "hello world"}, map[string]any{"op": "inject", "kind": "type", "text": "hello world"}, false},
		{"type-dash", []string{"type", "--", "-x"}, map[string]any{"op": "inject", "kind": "type", "text": "-x"}, false},
		{"press", []string{"press", "Super"}, map[string]any{"op": "inject", "kind": "press", "combo": "Super"}, false},
		{"release", []string{"release", "Super"}, map[string]any{"op": "inject", "kind": "release", "combo": "Super"}, false},
		{"click", []string{"click", "right"}, map[string]any{"op": "inject", "kind": "pointer", "cmd": "click", "args": []any{"right"}}, false},
		{"move", []string{"move", "12.5", "-3"}, map[string]any{"op": "inject", "kind": "pointer", "cmd": "move", "args": []any{"12.5", "-3"}}, false},
		{"move-negative", []string{"move", "--", "-640", "0"}, map[string]any{"op": "inject", "kind": "pointer", "cmd": "move", "args": []any{"-640", "0"}}, false},
		{"swipe", []string{"swipe", "4", "up"}, map[string]any{"op": "inject", "kind": "swipe", "fingers": float64(4), "dir": "up"}, false},
		{"query", []string{"query", "state"}, map[string]any{"op": "query", "what": "state"}, false},
		{"error", []string{"do", "missing"}, map[string]any{"op": "do", "action": "missing", "wait": "settled", "timeout": "10s"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "control.sock")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			t.Setenv("NEFERWL_CONTROL", path)
			result := make(chan error, 1)
			requests := make(chan map[string]any, 1)
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
				requests <- req
				reply := map[string]any{"id": req["id"], "ok": !tc.reject, "done": "committed", "state": map[string]any{"output": "A"}}
				if tc.reject {
					reply["error"] = "unknown-command"
				}
				result <- json.NewEncoder(conn).Encode(reply)
			}()
			code, stdout, stderr := execute(t, tc.args...)
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			req := <-requests
			if req["id"] != float64(1) {
				t.Fatalf("id: %v", req)
			}
			for key, want := range tc.want {
				if !reflect.DeepEqual(req[key], want) {
					t.Errorf("%s = %#v, want %#v", key, req[key], want)
				}
			}
			wantCode := 0
			if tc.reject {
				wantCode = 1
			}
			if code != wantCode || !strings.Contains(stdout, "\n  \"") {
				t.Fatalf("exit %d, stdout %s, stderr %s", code, stdout, stderr)
			}
			if tc.reject {
				if stderr != "unknown-command\n" {
					t.Fatal(stderr)
				}
			} else if stderr != "" {
				t.Fatal(stderr)
			}
		})
	}
}

// TestControlStateBare: neferwl state prints the state object alone, like
// the state file; query state keeps the envelope.
func TestControlStateBare(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("NEFERWL_CONTROL", path)
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
		if req["op"] != "query" || req["what"] != "state" {
			result <- fmt.Errorf("request %v", req)
			return
		}
		result <- json.NewEncoder(conn).Encode(map[string]any{"id": req["id"], "ok": true, "done": "committed", "state": map[string]any{"output": "A"}})
	}()
	code, stdout, stderr := execute(t, "state")
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if code != 0 || stdout != `{"output":"A"}`+"\n" || stderr != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestControlStateFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(`{"output":"fallback"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEFERWL_STATE", path)
	t.Setenv("NEFERWL_SESSION", "")
	t.Setenv("NEFERWL_CONTROL", "")
	code, out, errOut := execute(t, "state")
	if code != 0 || !strings.Contains(out, `"output":"fallback"`) || errOut != "" {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	t.Setenv("NEFERWL_CONTROL", filepath.Join(dir, "absent.sock"))
	code, out, errOut = execute(t, "state")
	if code != 1 || out != "" || !strings.Contains(errOut, "absent.sock") {
		t.Fatalf("explicit control must not fall back: %d %s %s", code, out, errOut)
	}
}

func TestControlClientIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
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
		dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)
		for range 2 {
			var req map[string]any
			if err := dec.Decode(&req); err != nil {
				result <- err
				return
			}
			if err := enc.Encode(map[string]any{"id": req["id"], "ok": true}); err != nil {
				result <- err
				return
			}
		}
		result <- nil
	}()
	env := map[string]string{"NEFERWL_CONTROL": path}
	c, err := dialControl(func(k string) string { return env[k] }, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.conn.Close()
	for i := uint64(1); i <= 2; i++ {
		reply, err := c.call(context.Background(), map[string]any{"op": "hello"})
		if err != nil {
			t.Fatal(err)
		}
		var id uint64
		if err := json.Unmarshal(reply["id"], &id); err != nil || id != i {
			t.Fatalf("%s %v", reply["id"], err)
		}
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestControlUsage(t *testing.T) {
	for _, args := range [][]string{{"do"}, {"do", "none", "--wait", "invalid"}, {"do", "none", "--timeout", "bad"}, {"do", "none", "--until", "bad"}, {"query", "other"}} {
		code, _, stderr := execute(t, args...)
		if code != 2 || stderr == "" {
			t.Fatalf("%v: %d %s", args, code, stderr)
		}
	}
	_, err := dialControl(func(string) string { return "" }, "")
	if err == nil || !strings.Contains(err.Error(), "NEFERWL_CONTROL") {
		t.Fatal(err)
	}
}
