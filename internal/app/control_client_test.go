package app

import (
	"encoding/json"
	"fmt"
	"github.com/bnema/neferwl/internal/domain"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// controlTestClient is a real JSON-lines socket client shared by integration
// tests. Write accepts the former key lines for compact multi-command fixtures;
// all delivery is through the production control protocol, not an input port.
type controlTestClient struct {
	conn net.Conn
	enc  *json.Encoder
	dec  *json.Decoder
	id   int
}

func dialTestControl(t *testing.T, runtime string) *controlTestClient {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(runtime, "neferwl", "sessions", "*", "control.sock"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("control paths %v: %v", paths, err)
	}
	conn, err := net.Dial("unix", paths[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &controlTestClient{conn: conn, enc: json.NewEncoder(conn), dec: json.NewDecoder(conn)}
}

func (c *controlTestClient) call(fields map[string]any) error {
	if err := c.conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	c.id++
	fields["id"] = c.id
	if err := c.enc.Encode(fields); err != nil {
		return err
	}
	var reply struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := c.dec.Decode(&reply); err != nil {
		return err
	}
	if !reply.OK {
		return fmt.Errorf("control: %s", reply.Error)
	}
	return nil
}

func (c *controlTestClient) Write(data []byte) (int, error) {
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		kind, arg, _ := strings.Cut(line, " ")
		req := map[string]any{"op": "inject", "kind": kind, "wait": "committed"}
		switch kind {
		case "key", "press", "release":
			req["combo"] = arg
		case "type":
			req["text"] = arg
		case "move", "click", "down", "up":
			req["kind"], req["cmd"], req["args"] = "pointer", kind, strings.Fields(arg)
		default:
			return 0, fmt.Errorf("unsupported fixture command %q", kind)
		}
		if err := c.call(req); err != nil {
			return 0, err
		}
	}
	return len(data), nil
}

//testpolicy:realtime real compositor and client mapping readiness
func awaitControlScene(t *testing.T, scenes <-chan []domain.Scene, done <-chan error, windows int) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case set := <-scenes:
			for _, scene := range set {
				if len(scene.Windows) == windows {
					return
				}
			}
		case err := <-done:
			if err != nil && strings.Contains(strings.ToLower(err.Error()), "vulkan") {
				t.Skip(err)
			}
			t.Fatalf("app exited: %v", err)
		case <-deadline:
			t.Fatal("scene readiness timeout")
		}
	}
}
