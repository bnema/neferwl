package cli

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestShotCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	t.Setenv("NEFERWL_CONTROL", path)
	requests := make(chan map[string]any, 1)
	result := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			result <- err
			return
		}
		defer func() { _ = conn.Close() }()
		var req map[string]any
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			result <- err
			return
		}
		requests <- req
		result <- json.NewEncoder(conn).Encode(map[string]any{"id": req["id"], "ok": true, "shots": []map[string]any{{"output": "A", "path": "/shots/7-A.png", "seq": 7, "png": []byte{1, 2, 3}}}})
	}()
	code, stdout, stderr := execute(t, "shot", "--output", "A", "--output", "B", "--inline", "--timeout", "3s")
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}
	req := <-requests
	if req["op"] != "shot" || req["inline"] != true || req["raw"] != false || req["timeout"] != "3s" || !reflect.DeepEqual(req["outputs"], []any{"A", "B"}) {
		t.Fatal(req)
	}
	if !strings.Contains(stdout, `"png": "AQID"`) || !strings.Contains(stdout, "/shots/7-A.png") {
		t.Fatal(stdout)
	}
}

func TestShotUsage(t *testing.T) {
	for _, args := range [][]string{{"shot", "--raw"}, {"shot", "--timeout", "0s"}, {"shot", "--timeout", "6m"}, {"do", "--timeout", "6m", "none"}, {"key", "--timeout", "6m", "a"}, {"shot", "unexpected"}, {"shot", "--output", ""}} {
		code, _, _ := execute(t, args...)
		if code != 2 {
			t.Fatalf("%v: %d", args, code)
		}
	}
}

// TestShotRelativeDir: a relative --dir is sent as an absolute path, resolved
// in the CLI's working directory rather than the session's.
func TestShotRelativeDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	t.Setenv("NEFERWL_CONTROL", path)
	t.Chdir(t.TempDir())
	requests := make(chan map[string]any, 1)
	result := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			result <- err
			return
		}
		defer func() { _ = conn.Close() }()
		var req map[string]any
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			result <- err
			return
		}
		requests <- req
		result <- json.NewEncoder(conn).Encode(map[string]any{"id": req["id"], "ok": true})
	}()
	code, _, stderr := execute(t, "shot", "--every-frame", "--dir", "frames")
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if req := <-requests; req["dir"] != filepath.Join(wd, "frames") || req["every_frame"] != true {
		t.Fatal(req)
	}
}
