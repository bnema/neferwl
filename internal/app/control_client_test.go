package app

import (
	"encoding/json"
	"fmt"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// controlTestClient is a real JSON-lines socket client shared by integration
// tests; input goes through the production control protocol, not an input
// port.
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

// inject sends one input request and waits until it is committed. kind is
// key, press or release (arg is the combo), type (arg is the text), or a
// pointer command move, click, down or up (arg holds its space-separated
// arguments).
func (c *controlTestClient) inject(kind, arg string) error {
	req := map[string]any{"op": "inject", "kind": kind, "wait": "committed"}
	switch kind {
	case "key", "press", "release":
		req["combo"] = arg
	case "type":
		req["text"] = arg
	case "move", "click", "down", "up":
		req["kind"], req["cmd"], req["args"] = "pointer", kind, strings.Fields(arg)
	default:
		return fmt.Errorf("unsupported inject kind %q", kind)
	}
	return c.call(req)
}

// injectAll sends "kind arg" steps in order with inject.
func injectAll(c *controlTestClient, steps ...string) error {
	for _, step := range steps {
		kind, arg, _ := strings.Cut(step, " ")
		if err := c.inject(kind, arg); err != nil {
			return err
		}
	}
	return nil
}

// noTerminal is the --no-terminal override: no terminal at startup.
func noTerminal() core.PlanOverrides {
	off := false
	return core.PlanOverrides{AutoTerminal: &off}
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
