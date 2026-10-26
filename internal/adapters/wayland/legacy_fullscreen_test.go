package wayland

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/ports"
)

// outputSizedServer runs a server whose only output is w×h logical.
func outputSizedServer(t *testing.T, w, h int) (*Server, chan ports.ClientEvent, string) {
	t.Helper()
	dir := t.TempDir()
	events := make(chan ports.ClientEvent, 16)
	out := ports.Layout{{Info: ports.OutputInfo{Name: "HEADLESS-1", Width: w, Height: h}, Width: w, Height: h, Scale: 1}}
	s, err := New(Options{RuntimeDir: dir, Outputs: out}, Channels{Events: events, Commands: make(chan ports.ClientCommand, 16)}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	run := guardedServerRun(t, s, ctx)
	go func() { done <- run() }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return s, events, dir
}

// A tiled toplevel whose first committed size is its output's size reports
// it once (core then fullscreens it); a smaller one reports nothing.
func TestTiledFirstSizeAtOutputSizeEmitsResized(t *testing.T) {
	s, events, dir := outputSizedServer(t, 1, 1)
	c := protocolClient(t, s, dir)
	w := toplevelMapper(t, c, events)()
	if w.Floating {
		t.Fatal("expected a tiled window")
	}
	ev := waitEvent(t, events, 2*time.Second)
	if r, ok := ev.(ports.WindowResized); !ok || r.ID != w.ID || r.Width != 1 || r.Height != 1 {
		t.Fatalf("got %#v, want WindowResized 1x1 for %d", ev, w.ID)
	}
}

func TestTiledSmallerThanOutputEmitsNoResized(t *testing.T) {
	s, events, dir := outputSizedServer(t, 2, 2)
	c := protocolClient(t, s, dir)
	toplevelMapper(t, c, events)()
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		t.Fatalf("unexpected event %#v", ev)
	case <-waylandDeadline(200 * time.Millisecond):
	}
}
