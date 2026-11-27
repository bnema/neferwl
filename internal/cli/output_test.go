package cli

import (
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutputCommands(t *testing.T) {
	for _, tc := range []struct {
		test, op, name, id string
		args               []string
		sent               bool // edid_id is in the request
	}{
		{"add", "output-add", "X", "", []string{"output", "add", "1920x1080@60", "--name", "X"}, false},
		{"add with identity", "output-add", "X", "aa01", []string{"output", "add", "1920x1080@60", "--name", "X", "--edid-id", "aa01"}, true},
		{"add with empty identity", "output-add", "X", "", []string{"output", "add", "1920x1080@60", "--name", "X", "--edid-id="}, true},
		{"rm", "output-rm", "X", "", []string{"output", "rm", "X"}, false},
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
				if req["op"] != tc.op || req["name"] != tc.name || sent != tc.sent || (sent && id != tc.id) {
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

// --edid-id applies to output add only; rm refuses it before dialing.
func TestOutputRmRejectsEDIDID(t *testing.T) {
	t.Setenv("NEFERWL_CONTROL", filepath.Join(t.TempDir(), "absent.sock"))
	code, _, stderr := execute(t, "output", "rm", "X", "--edid-id", "aa01")
	if code == 0 || !strings.Contains(stderr, "--edid-id applies to output add only") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
}
