package cli

import (
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
)

func TestOutputCommands(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		op, name string
	}{{[]string{"output", "add", "1920x1080@60", "--name", "X"}, "output-add", "X"}, {[]string{"output", "rm", "X"}, "output-rm", "X"}} {
		t.Run(tc.op, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "control.sock")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			t.Setenv("NEFERWL_CONTROL", path)
			done := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				var req map[string]any
				err = json.NewDecoder(conn).Decode(&req)
				if req["op"] != tc.op || req["name"] != tc.name {
					t.Error(req)
				}
				if err == nil {
					err = json.NewEncoder(conn).Encode(map[string]any{"id": req["id"], "ok": true})
				}
				done <- err
			}()
			code, _, stderr := execute(t, tc.args...)
			if code != 0 {
				t.Fatal(stderr)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
