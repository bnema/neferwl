package core

import (
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func wantOutbox(t *testing.T, got []ports.ClientCommand, want ...ports.ClientCommand) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("commands %#v, want %#v", got, want)
	}
}

// takeAll returns what the sender would deliver, in order.
func takeAll(o *outbox) []ports.ClientCommand {
	var got []ports.ClientCommand
	for _, e := range o.take() {
		got = append(got, e.command())
	}
	return got
}

// Only adjacent motions for the same window merge: the position is the
// latest, the deltas add up. Everything else keeps its place.
func TestOutboxMergesAdjacentMotionOnly(t *testing.T) {
	o := newOutbox()
	// Nobody receives, so every command queues.
	out := make(chan ports.ClientCommand)
	push := func(v ports.ClientCommand) { o.push(out, v, ports.SecurityState{}, false) }
	push(ports.PointerMotionTo{ID: 1, X: 1, Y: 1, DX: 1, DY: 2, UnaccelDX: 3, UnaccelDY: 4})
	push(ports.PointerMotionTo{ID: 1, X: 2, Y: 2, DX: 10, DY: 20, UnaccelDX: 30, UnaccelDY: 40})
	push(ports.PointerButtonTo{ID: 1, Button: 0x110, Pressed: true})
	push(ports.PointerMotionTo{ID: 1, X: 3, Y: 3, DX: 5})
	push(ports.PointerMotionTo{ID: 2, X: 4, Y: 4, DX: 7})
	push(ports.FocusWindow{ID: 2})
	push(ports.PointerMotionTo{ID: 2, X: 5, Y: 5, DX: 1})
	push(ports.PointerMotionTo{ID: 2, X: 6, Y: 6, DX: 1})
	want := []ports.ClientCommand{
		ports.PointerMotionTo{ID: 1, X: 2, Y: 2, DX: 11, DY: 22, UnaccelDX: 33, UnaccelDY: 44},
		ports.PointerButtonTo{ID: 1, Button: 0x110, Pressed: true},
		ports.PointerMotionTo{ID: 1, X: 3, Y: 3, DX: 5},
		ports.PointerMotionTo{ID: 2, X: 4, Y: 4, DX: 7},
		ports.FocusWindow{ID: 2},
		ports.PointerMotionTo{ID: 2, X: 6, Y: 6, DX: 2},
	}
	wantOutbox(t, takeAll(&o), want...)
}

// Wrapped motions merge only under an identical owner envelope, and keep it.
func TestOutboxMergesSecuredMotionPerEnvelope(t *testing.T) {
	o := newOutbox()
	out := make(chan ports.ClientCommand)
	a := ports.SecurityState{Generation: 1}
	b := ports.SecurityState{Generation: 2}
	o.push(out, ports.PointerMotionTo{ID: 1, X: 1, DX: 1}, a, true)
	o.push(out, ports.PointerMotionTo{ID: 1, X: 2, DX: 2}, a, true)
	o.push(out, ports.PointerMotionTo{ID: 1, X: 3, DX: 4}, b, true)
	o.push(out, ports.PointerMotionTo{ID: 1, X: 4, DX: 8}, ports.SecurityState{}, false)
	wantOutbox(t, takeAll(&o),
		ports.SecurityCommand{State: a, Command: ports.PointerMotionTo{ID: 1, X: 2, DX: 3}},
		ports.SecurityCommand{State: b, Command: ports.PointerMotionTo{ID: 1, X: 3, DX: 4}},
		ports.PointerMotionTo{ID: 1, X: 4, DX: 8},
	)
}

// With room in the channel and nothing queued, a command goes straight out.
func TestOutboxSendsDirectlyWhileDisplayKeepsUp(t *testing.T) {
	o := newOutbox()
	out := make(chan ports.ClientCommand, 4)
	o.push(out, ports.PointerMotionTo{ID: 1, DX: 1}, ports.SecurityState{}, false)
	o.push(out, ports.PointerMotionTo{ID: 1, DX: 2}, ports.SecurityState{}, false)
	if len(out) != 2 {
		t.Fatalf("%d commands sent, want 2 unmerged", len(out))
	}
	if got := o.take(); len(got) != 0 {
		t.Fatalf("queued %d", len(got))
	}
}
