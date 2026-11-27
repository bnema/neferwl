package cli

import (
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
)

func TestOutputCommands(t *testing.T) {
	for _, tc := range []struct {
		test, op, name, id string
		args               []string
	}{
		{"add", "output-add", "X", "", []string{"output", "add", "1920x1080@60", "--name", "X"}},
		{"add with identity", "output-add", "X", "aa01", []string{"output", "add", "1920x1080@60", "--name", "X", "--edid-id", "aa01"}},
		{"rm", "output-rm", "X", "", []string{"output", "rm", "X"}},
	} {
		t.Run(tc.test, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "control.sock")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			t.Setenv("NEFERWL_CONTROL", path)
			done := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer func() { _ = conn.Close() }()
				var req map[string]any
				if err := json.NewDecoder(conn).Decode(&req); err != nil {
					done <- err
					return
				}
				id, sent := req["edid_id"]
				if req["op"] != tc.op || req["name"] != tc.name || sent != (tc.id != "") || (sent && id != tc.id) {
					t.Error(req)
				}
				done <- json.NewEncoder(conn).Encode(map[string]any{"id": req["id"], "ok": true})
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
