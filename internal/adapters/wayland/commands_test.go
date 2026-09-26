package wayland

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
)

func TestDrainCommandsCollapsesMotion(t *testing.T) {
	cmds := make(chan ports.ClientCommand, 16)
	cmds <- ports.PointerMotionTo{ID: 1, X: 2, DX: 0.25, UnaccelDX: 0.5}
	cmds <- ports.PointerMotionTo{ID: 1, X: 3, DX: 0.25, DY: -0.5, UnaccelDX: 0.5, UnaccelDY: -1}
	cmds <- ports.PointerButtonTo{ID: 1, Button: 272, Pressed: true}
	cmds <- ports.PointerMotionTo{ID: 1, X: 4}
	cmds <- ports.PointerFocus{ID: 2}
	cmds <- ports.PointerMotionTo{ID: 2, X: 5}
	cmds <- ports.PointerMotionTo{ID: 2, X: 6}
	batch, open := drainCommands(ports.PointerMotionTo{ID: 1, X: 1, DX: 0.25, UnaccelDX: 0.5}, cmds)
	want := []ports.ClientCommand{
		// Slow motions add up: none of the sub-pixel deltas is lost.
		ports.PointerMotionTo{ID: 1, X: 3, DX: 0.75, DY: -0.5, UnaccelDX: 1.5, UnaccelDY: -1},
		ports.PointerButtonTo{ID: 1, Button: 272, Pressed: true},
		ports.PointerMotionTo{ID: 1, X: 4},
		ports.PointerFocus{ID: 2},
		ports.PointerMotionTo{ID: 2, X: 6},
	}
	if !open || !reflect.DeepEqual(batch, want) {
		t.Fatalf("open=%v batch=%#v", open, batch)
	}
	close(cmds)
	if _, open := drainCommands(ports.PointerFocus{}, cmds); open {
		t.Fatal("closed channel reported open")
	}
}

// Each command used to cost one display.Do round trip (up to one 5 ms
// event-loop dispatch), capping pointer motion at ~150/s. A 1 kHz mouse
// must be absorbed without core blocking.
func TestCommandThroughput(t *testing.T) {
	cmds := make(chan ports.ClientCommand, 32)
	s, err := New(Options{RuntimeDir: t.TempDir(), Outputs: testOutputs}, Channels{Commands: cmds}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() { cancel(); <-done }()
	start := time.Now()
	for i := range 2000 {
		cmds <- ports.PointerMotionTo{ID: 1, X: float64(i), Y: 1}
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("2000 motion commands took %v (%.0f/s); want well above 1000/s", d, 2000/d.Seconds())
	}
}
