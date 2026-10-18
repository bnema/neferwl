package core

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"
	"time"

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

// A display that reads nothing for long fills the outbox to its limit, then
// core waits for room: the queue never grows past it, nothing is dropped.
func TestOutboxIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		out := make(chan ports.ClientCommand)
		c := &Core{out: newOutbox(), ch: Channels{Commands: out}}
		c.out.start()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go c.out.run(ctx, out)
		done := make(chan error, 1)
		go func() {
			for i := range outboxLimit + 10 {
				// Not mergeable: each takes a place.
				if err := c.command(ctx, ports.FocusWindow{ID: WindowID(i)}); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}()
		synctest.Wait()
		if n := c.out.backlog(); n != outboxLimit+1 {
			t.Fatalf("backlog %d, want the limit and the one waiting", n)
		}
		select {
		case err := <-done:
			t.Fatalf("pushed past the limit: %v", err)
		default:
		}
		for i := range outboxLimit + 10 {
			if got := (<-out).(ports.FocusWindow); got.ID != WindowID(i) {
				t.Fatalf("command %d is %+v", i, got)
			}
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

// Queuing a command and delivering it allocates nothing once warm: the
// batches are recycled and flush's progress signal is reused.
func TestOutboxQueuedDeliveryAllocations(t *testing.T) {
	o := newOutbox()
	full := make(chan ports.ClientCommand, 1)
	full <- ports.FocusWindow{}
	sink := make(chan ports.ClientCommand, 8)
	var cmd ports.ClientCommand = ports.FocusWindow{ID: 1}
	var state ports.SecurityState
	ctx := context.Background()
	cycle := func() {
		// full has no room: push queues, even for a secured command.
		o.push(full, cmd, state, false)
		o.push(full, cmd, state, true)
		o.deliver(ctx, sink, o.take())
		<-sink
		<-sink
		select {
		case <-o.progress:
		default:
			t.Fatal("delivery left no progress signal")
		}
	}
	cycle()
	// The secured delivery boxes its envelope once; the failed direct send
	// must not box another.
	if n := testing.AllocsPerRun(100, cycle); n > 1 {
		t.Fatalf("%v allocations per queued delivery, want at most 1", n)
	}
}

// The stats tell direct sends from queued ones, count merges, and time how
// long the first queued command waited for the channel and core in flush.
func TestOutboxStats(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := newOutbox()
		out := make(chan ports.ClientCommand, 1)
		ctx := context.Background()
		var none ports.SecurityState
		o.push(out, ports.FocusWindow{ID: 1}, none, false) // direct: room
		o.push(out, ports.PointerMotionTo{ID: 1, DX: 1}, none, false)
		time.Sleep(5 * time.Millisecond)
		seq := o.push(out, ports.PointerMotionTo{ID: 1, DX: 2}, none, false) // merges
		time.Sleep(5 * time.Millisecond)
		go func() {
			time.Sleep(20 * time.Millisecond)
			<-out
		}()
		if err := o.flush(ctx, out, seq); err != nil {
			t.Fatal(err)
		}
		s := o.takeStats()
		want := CommandStats{Direct: 1, Queued: 1, Merged: 1, MaxBacklog: 1, MaxQueueWait: 30 * time.Millisecond, MaxFlushWait: 20 * time.Millisecond}
		if s != want {
			t.Fatalf("stats %+v, want %+v", s, want)
		}
		if s := o.takeStats(); s != (CommandStats{}) {
			t.Fatalf("not reset: %+v", s)
		}
	})
}
