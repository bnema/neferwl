package app

import (
	"context"
	"encoding/json"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/statewire"
	"github.com/bnema/neferwl/internal/domain"
)

//testpolicy:realtime Real headless GPU workers and control socket exchanges use bounded I/O waits.
func TestHeadlessControlEndToEnd(t *testing.T) {
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	cfg := config.Defaults()
	cfg.Terminal.Command = []string{os.Args[0], spawnClientArg}
	scenes := make(chan []domain.Scene, 4096)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Backend: "headless", NoXwayland: true, PlanOverrides: noTerminal(), Config: cfg, Sizes: [][2]int{{640, 480}}, testScenes: scenes})
	}()
	exited := false
	t.Cleanup(func() {
		cancel()
		if !exited {
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("Run: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("Run did not stop")
			}
		}
	})
	select {
	case <-scenes:
	case err := <-done:
		exited = true
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "vulkan") {
			t.Skipf("Vulkan unavailable: %v", err)
		}
		t.Fatalf("app exited: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("initial scene missing")
	}
	paths, err := filepath.Glob(filepath.Join(runtime, "neferwl", "sessions", "*", "control.sock"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("control paths: %v %v", paths, err)
	}
	conn, err := net.Dial("unix", paths[0])
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)
	call := func(id int, op string, fields map[string]any) map[string]json.RawMessage {
		t.Helper()
		fields["id"] = id
		fields["op"] = op
		if err := enc.Encode(fields); err != nil {
			t.Fatal(err)
		}
		var result map[string]json.RawMessage
		if err := dec.Decode(&result); err != nil {
			t.Fatal(err)
		}
		if string(result["ok"]) != "true" {
			t.Fatalf("%s: %s", op, result)
		}
		return result
	}
	shot := call(99, "shot", map[string]any{"outputs": []string{"HEADLESS-1"}, "timeout": "5s"})
	var shots []struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(shot["shots"], &shots); err != nil || len(shots) != 1 {
		t.Fatalf("shots: %s %v", shot, err)
	}
	f, err := os.Open(shots[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	image, err := png.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if image.Bounds().Dx() != 640 || image.Bounds().Dy() != 480 {
		t.Fatal(image.Bounds())
	}
	added := call(100, "output-add", map[string]any{"mode": "800x600@60"})
	checkOutput := func(reply map[string]json.RawMessage, want bool) {
		t.Helper()
		var state statewire.State
		if err := json.Unmarshal(reply["state"], &state); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, output := range state.Outputs {
			if output.Name == "HEADLESS-2" {
				found = true
			}
		}
		if found != want {
			t.Fatalf("HEADLESS-2 present=%v want %v: %s", found, want, reply["state"])
		}
	}
	checkOutput(added, true)
	checkOutput(call(101, "query", map[string]any{"what": "state"}), true)
	checkOutput(call(102, "output-rm", map[string]any{"name": "HEADLESS-2"}), false)
	checkOutput(call(103, "query", map[string]any{"what": "state"}), false)
	hello := call(1, "hello", map[string]any{})
	if string(hello["v"]) != "1" {
		t.Fatal(hello)
	}
	decodeState := func(reply map[string]json.RawMessage) statewire.State {
		t.Helper()
		var state statewire.State
		if err := json.Unmarshal(reply["state"], &state); err != nil {
			t.Fatal(err)
		}
		if len(state.Outputs) != 1 {
			t.Fatalf("outputs: %+v", state)
		}
		return state
	}
	// Numbered navigation stops at the end; a mapped window creates the
	// trailing empty workspace that focus-workspace-next can select.
	call(2, "do", map[string]any{"action": "spawn-terminal"})
	ready := time.After(10 * time.Second)
	mapped := false
	for !mapped {
		select {
		case set := <-scenes:
			mapped = len(set) == 1 && len(set[0].Windows) > 0
		case <-ready:
			t.Fatal("spawned window did not map")
		}
	}

	before := decodeState(call(2, "query", map[string]any{"what": "state"}))
	after := decodeState(call(3, "do", map[string]any{"action": "focus-workspace-next"}))
	if after.Outputs[0].Active != before.Outputs[0].Active+1 {
		t.Fatalf("workspace did not advance: before %+v after %+v", before.Outputs, after.Outputs)
	}
	queried := decodeState(call(4, "query", map[string]any{"what": "state"}))
	if queried.Outputs[0].Active != after.Outputs[0].Active {
		t.Fatalf("query state mismatch: %+v", queried)
	}
	cancel()
	select {
	case err := <-done:
		exited = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown blocked")
	}
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Fatalf("socket remains: %v", err)
	}
}
