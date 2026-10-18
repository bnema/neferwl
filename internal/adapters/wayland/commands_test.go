package wayland

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/ports"
)

// Adjacent motions for one window fold into the last queued command, summing
// the relative deltas; nothing is merged across another command, and the
// order of arrival is kept.
func TestCommandRingCollapsesMotionInOrder(t *testing.T) {
	s, _, _, _, _, _ := presentServer(t)
	locked := ports.SecurityState{Generation: 2, Protected: true}
	unlocked := ports.SecurityState{Generation: 3}
	var got []ports.ClientCommand // display goroutine only
	r := newCommandRing(s.display, func(q queuedCommand) { got = append(got, q.command()) })
	release := make(chan struct{})
	stalled := make(chan struct{})
	s.display.Post(func() { close(stalled); <-release })
	<-stalled
	for _, c := range []ports.ClientCommand{
		ports.PointerMotionTo{ID: 1, X: 1, DX: 0.25, UnaccelDX: 0.5},
		ports.PointerMotionTo{ID: 1, X: 2, DX: 0.25, UnaccelDX: 0.5},
		ports.PointerMotionTo{ID: 1, X: 3, DX: 0.25, DY: -0.5, UnaccelDX: 0.5, UnaccelDY: -1},
		ports.PointerButtonTo{ID: 1, Button: 272, Pressed: true},
		ports.PointerMotionTo{ID: 1, X: 4},
		ports.PointerFocus{ID: 2},
		ports.PointerMotionTo{ID: 2, X: 5},
		ports.PointerMotionTo{ID: 2, X: 6},
		ports.PointerMotionTo{ID: 1, X: 7}, // other window: not adjacent-same
		// Enveloped, as core sends them: same state merges, a different
		// state, a bare motion, or another window does not.
		ports.SecurityCommand{State: locked, Command: ports.PointerMotionTo{ID: 1, X: 8, DX: 1, UnaccelDY: 2}},
		ports.SecurityCommand{State: locked, Command: ports.PointerMotionTo{ID: 1, X: 9, DX: 2, UnaccelDY: 3}},
		ports.SecurityCommand{State: locked, Command: ports.PointerMotionTo{ID: 1, X: 10, DY: 4}},
		ports.SecurityCommand{State: unlocked, Command: ports.PointerMotionTo{ID: 1, X: 11, DX: 1}},
		ports.SecurityCommand{State: unlocked, Command: ports.PointerMotionTo{ID: 1, X: 12, DX: 1}},
		ports.PointerMotionTo{ID: 1, X: 13},
		ports.SecurityCommand{State: unlocked, Command: ports.PointerMotionTo{ID: 1, X: 14}},
		ports.SecurityCommand{State: unlocked, Command: ports.PointerMotionTo{ID: 2, X: 15}},
		ports.SecurityCommand{State: unlocked, Command: ports.PointerButtonTo{ID: 2}},
	} {
		if !r.Push(queue(c)) {
			t.Fatal("push refused")
		}
	}
	close(release)
	want := []ports.ClientCommand{
		// Slow motions add up: none of the sub-pixel deltas is lost.
		ports.PointerMotionTo{ID: 1, X: 3, DX: 0.75, DY: -0.5, UnaccelDX: 1.5, UnaccelDY: -1},
		ports.PointerButtonTo{ID: 1, Button: 272, Pressed: true},
		ports.PointerMotionTo{ID: 1, X: 4},
		ports.PointerFocus{ID: 2},
		ports.PointerMotionTo{ID: 2, X: 6},
		ports.PointerMotionTo{ID: 1, X: 7},
		ports.SecurityCommand{State: locked, Command: ports.PointerMotionTo{ID: 1, X: 10, DX: 3, DY: 4, UnaccelDY: 5}},
		ports.SecurityCommand{State: unlocked, Command: ports.PointerMotionTo{ID: 1, X: 12, DX: 2}},
		ports.PointerMotionTo{ID: 1, X: 13},
		ports.SecurityCommand{State: unlocked, Command: ports.PointerMotionTo{ID: 1, X: 14}},
		ports.SecurityCommand{State: unlocked, Command: ports.PointerMotionTo{ID: 2, X: 15}},
		ports.SecurityCommand{State: unlocked, Command: ports.PointerButtonTo{ID: 2}},
	}
	var final []ports.ClientCommand
	s.display.Do(func() { final = got }) // runs after the drain posted before it
	if !reflect.DeepEqual(final, want) {
		t.Fatalf("applied %#v, want %#v", final, want)
	}
}

// A merge needs no room: motion keeps folding into the last queued command
// while the ring is full of other commands, and the push does not block.
func TestCommandRingMergesWhenFull(t *testing.T) {
	s, _, _, _, _, _ := presentServer(t)
	var got []ports.ClientCommand
	r := newCommandRing(s.display, func(q queuedCommand) { got = append(got, q.command()) })
	release := make(chan struct{})
	stalled := make(chan struct{})
	s.display.Post(func() { close(stalled); <-release })
	<-stalled
	for i := range commandRingSize - 1 {
		r.Push(queue(ports.PointerFocus{ID: ports.WindowID(i)}))
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 { // fills the ring with one motion, then folds the rest
			r.Push(queue(ports.PointerMotionTo{ID: 1, DX: 1}))
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("motion push blocked on a full ring")
	}
	close(release)
	var last ports.ClientCommand
	s.display.Do(func() { last = got[len(got)-1] })
	if m, ok := last.(ports.PointerMotionTo); !ok || m.DX != 1000 {
		t.Fatalf("last applied %#v, want one motion with DX 1000", last)
	}
}

// A busy display goroutine must not stall the forwarder: it keeps draining
// the Commands channel into the ring up to the ring capacity, and only then
// does it wait (and core with it, through the full channel).
func TestForwarderDrainsWhileDisplayBusy(t *testing.T) {
	cmds := make(chan ports.ClientCommand, 32)
	s, err := New(Options{RuntimeDir: t.TempDir(), Outputs: testOutputs}, Channels{Commands: cmds}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() { cancel(); <-done }()

	release := make(chan struct{})
	stalled := make(chan struct{})
	s.display.Post(func() { close(stalled); <-release })
	<-stalled

	// Not mergeable: every command takes a slot.
	const total = commandRingSize + 100
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		for i := range total {
			select {
			case cmds <- ports.PointerFocus{ID: ports.WindowID(i + 1)}:
			case <-ctx.Done():
				return
			}
		}
	}()
	queued := func() int {
		s.commands.mu.Lock()
		defer s.commands.mu.Unlock()
		return len(s.commands.ring)
	}
	// Ring full, one command in the blocked forwarder, a full channel.
	deadline := time.Now().Add(5 * time.Second)
	for queued() != commandRingSize || len(cmds) != cap(cmds) {
		if time.Now().After(deadline) {
			t.Fatalf("ring holds %d (want %d), channel %d (want %d)", queued(), commandRingSize, len(cmds), cap(cmds))
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-sent:
		t.Fatal("sender finished although the ring is full")
	default:
	}
	close(release)
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("forwarder did not resume after the display was released")
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
