package wayland

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/ports"
)

// linkServer is a running server with no client, to publish contents on its
// display goroutine.
func linkServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Options{RuntimeDir: t.TempDir(), Outputs: testOutputs}, Channels{}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	run := guardedServerRun(t, s, ctx)
	go func() { done <- run() }()
	t.Cleanup(func() { cancel(); <-done })
	return s
}

func solid(id ports.WindowID) ports.SurfaceContent {
	return ports.SurfaceContent{ID: id, Solid: &ports.SolidColor{}}
}

func (s *Server) publish(t *testing.T, contents ...ports.SurfaceContent) {
	t.Helper()
	if !s.display.Do(func() {
		for _, c := range contents {
			s.emitContent(c, damage{full: true})
		}
	}) {
		t.Fatal("display stopped")
	}
}

//testpolicy:realtime Server.Run waits on its libwayland epoll fd through the netpoller and Do/Post wake it with an eventfd; that wait is not durably blocked, so synctest.Wait hangs even with no client socket (verified)
func nextBatch(t *testing.T, in <-chan *ports.ContentBatch) *ports.ContentBatch {
	t.Helper()
	select {
	case b := <-in:
		return b
	case <-waylandDeadline(2 * time.Second):
		t.Fatal("no batch")
		return nil
	}
}

// noBatch asserts that no batch is on the channel once the display goroutine
// went through everything posted so far (a Return's flush included): sends
// happen only there, so there is nothing left to wait for.
func noBatch(t *testing.T, s *Server, in <-chan *ports.ContentBatch) {
	t.Helper()
	s.display.Do(func() {})
	select {
	case b := <-in:
		t.Fatalf("unexpected batch %+v", b.Items)
	default:
	}
}

// Contents published while the output has not taken the previous batch
// replace each other per window: the output gets the first batch, then one
// with only the newest content, once it returned the first. (Ordering per
// window holds because a batch holds one entry per window and batches are
// FIFO; the release accounting needs only the newest Seq.)
func TestContentBatchKeepsLatestPerWindow(t *testing.T) {
	s := linkServer(t)
	in, ret := s.AttachOutput("A")
	s.publish(t, solid(1))                     // sent at once
	s.publish(t, solid(1), solid(2), solid(1)) // wait for the output
	first := nextBatch(t, in)
	if len(first.Items) != 1 || first.Items[0].ID != 1 || first.Items[0].Seq != 1 {
		t.Fatalf("first batch %+v", first.Items)
	}
	noBatch(t, s, in) // nothing moves before the output returns a batch...
	first.Reset()
	ret.Return(first) // ...which sends the waiting contents
	second := nextBatch(t, in)
	if len(second.Items) != 2 {
		t.Fatalf("second batch %+v", second.Items)
	}
	for _, c := range second.Items {
		switch c.ID {
		case 1:
			if c.Seq != 3 {
				t.Errorf("window 1 seq %d, want the newest (3)", c.Seq)
			}
		case 2:
			if c.Seq != 1 {
				t.Errorf("window 2 seq %d, want 1", c.Seq)
			}
		default:
			t.Errorf("unexpected window %d", c.ID)
		}
	}
}

// An output attached later starts with the latest content of every live
// window and nothing for windows that were emptied.
func TestContentBatchAttachSeedsLiveWindows(t *testing.T) {
	s := linkServer(t)
	s.publish(t, solid(1), solid(2), solid(3), ports.SurfaceContent{ID: 2}, solid(1))
	in, _ := s.AttachOutput("A")
	b := nextBatch(t, in)
	got := map[ports.WindowID]uint64{}
	for _, c := range b.Items {
		got[c.ID] = c.Seq
	}
	if len(got) != 2 || len(b.Items) != 2 || got[1] != 2 || got[3] != 1 {
		t.Fatalf("seed batch %+v", b.Items)
	}
	// An attach with nothing to show sends nothing.
	s.display.Do(func() { clear(s.latest) })
	in2, _ := s.AttachOutput("B")
	noBatch(t, s, in2)
}

// An empty content reaches the attached outputs (it releases the window's
// buffers) and removes the window from what a later output starts with.
func TestContentBatchDeliversEmpty(t *testing.T) {
	s := linkServer(t)
	in, ret := s.AttachOutput("A")
	s.publish(t, solid(1))
	b := nextBatch(t, in)
	b.Reset()
	ret.Return(b)
	s.publish(t, ports.SurfaceContent{ID: 1})
	b = nextBatch(t, in)
	if len(b.Items) != 1 || b.Items[0].ID != 1 || !b.Items[0].Empty() || b.Items[0].Seq != 2 {
		t.Fatalf("empty not delivered: %+v", b.Items)
	}
	var left int
	s.display.Do(func() { left = len(s.latest) })
	if left != 0 {
		t.Fatalf("latest keeps %d emptied windows", left)
	}
	// Superseded by an empty before the output looked: the empty wins.
	b.Reset()
	ret.Return(b)
	s.publish(t, solid(1))                              // sent
	s.publish(t, solid(1), ports.SurfaceContent{ID: 1}) // pending: last one is empty
	first := nextBatch(t, in)
	first.Reset()
	ret.Return(first)
	b = nextBatch(t, in)
	if len(b.Items) != 1 || !b.Items[0].Empty() || b.Items[0].Seq != 5 {
		t.Fatalf("emptied window: %+v", b.Items)
	}
}

// Every attached output gets its own batches; detaching one stops its feed
// only, and its output may still Return what it holds.
func TestContentBatchDetach(t *testing.T) {
	s := linkServer(t)
	inA, retA := s.AttachOutput("A")
	inB, _ := s.AttachOutput("B")
	s.publish(t, solid(1))
	a, b := nextBatch(t, inA), nextBatch(t, inB)
	if len(a.Items) != 1 || len(b.Items) != 1 || a == b {
		t.Fatalf("batches %+v %+v", a.Items, b.Items)
	}
	s.DetachOutput(retA)
	// Waiting contents of a detached link are never sent; Return is safe.
	a.Reset()
	retA.Return(a)
	s.publish(t, solid(1))
	noBatch(t, s, inA)
	if got := nextBatch(t, inB); len(got.Items) != 1 {
		t.Fatalf("B batch %+v", got.Items)
	}
	// Attaching under a known name replaces the link.
	inA2, _ := s.AttachOutput("A")
	if got := nextBatch(t, inA2); len(got.Items) != 1 {
		t.Fatalf("re-attached batch %+v", got.Items)
	}
	var links int
	s.display.Do(func() { links = len(s.links) })
	if links != 2 {
		t.Fatalf("%d links, want 2", links)
	}
}

// Detaching a feed by its handle never removes a newer feed of the same
// name: A attached again replaced the first link, and detaching the old
// handle leaves the new one fed.
func TestDetachOldHandleKeepsReplacedLink(t *testing.T) {
	s := linkServer(t)
	_, oldRet := s.AttachOutput("A")
	in, _ := s.AttachOutput("A")
	s.DetachOutput(oldRet)
	var links int
	s.display.Do(func() { links = len(s.links) })
	if links != 1 {
		t.Fatalf("%d links, want the new one only", links)
	}
	s.publish(t, solid(1))
	if b := nextBatch(t, in); len(b.Items) != 1 {
		t.Fatalf("new link batch %+v", b.Items)
	}
}

// Emit, send, consume and return a batch without allocating once the
// batches have their capacity. The consumer runs inline, like an output that
// keeps up.
func TestContentBatchCycleAllocations(t *testing.T) {
	s := &Server{
		contentSeq: map[ports.WindowID]uint64{},
		damage:     map[ports.WindowID][]ports.SeqDamage{},
		latest:     map[ports.WindowID]ports.SurfaceContent{},
	}
	in, ret := s.attachOutput("A")
	c := solid(1)
	other := solid(2)
	cycle := func() {
		for _, c := range []ports.SurfaceContent{c, other} {
			s.emitContent(c, damage{full: true})
			select {
			case b := <-in:
				for i := range b.Items {
					_ = b.Items[i].Seq
				}
				b.Reset()
				ret.Return(b)
			default:
				panic("no batch sent")
			}
		}
	}
	for range 3 * damageHistoryCap {
		cycle()
	}
	if allocs := testing.AllocsPerRun(200, cycle); allocs != 0 {
		t.Errorf("content batch cycle: %.2f allocs, want 0", allocs)
	}
}

// A late-attached output gets all the live windows in one batch, however
// many there are, and AttachOutput returns without the output reading.
//
//testpolicy:realtime real Wayland unix socket between the display loop and a wlturbo client; kernel readiness is not a durably blocked bubble operation
func TestContentBatchAttachReplaysLargeSet(t *testing.T) {
	s := linkServer(t)
	const windows = 100
	all := make([]ports.SurfaceContent, 0, windows)
	for id := 1; id <= windows; id++ {
		all = append(all, solid(ports.WindowID(id)))
	}
	s.publish(t, all...)
	attached := make(chan (<-chan *ports.ContentBatch), 1)
	go func() { in, _ := s.AttachOutput("A"); attached <- in }()
	var in <-chan *ports.ContentBatch
	select {
	case in = <-attached:
	case <-waylandDeadline(2 * time.Second):
		t.Fatal("AttachOutput waits for the output to read")
	}
	b := nextBatch(t, in)
	ids := map[ports.WindowID]struct{}{}
	for _, c := range b.Items {
		ids[c.ID] = struct{}{}
	}
	if len(b.Items) != windows || len(ids) != windows {
		t.Fatalf("replay batch holds %d items, %d distinct windows, want %d", len(b.Items), len(ids), windows)
	}
}

// The output returns batches in bursts and holds some while content keeps
// coming, so the channel is full and no batch is free when emitContent runs;
// Returns race with it from another goroutine. Whatever the interleaving, the
// output ends up with the newest Seq of every window (the pending/flushPosted
// handshake never strands content). Run with -race -count=20.
//
//testpolicy:realtime real Wayland unix socket between the display loop and a wlturbo client; kernel readiness is not a durably blocked bubble operation
func TestContentBatchHandshakeNeverStrandsContent(t *testing.T) {
	s := linkServer(t)
	in, ret := s.AttachOutput("A")
	const windows, rounds = 4, 3000
	progress := make(chan struct{}, 1)
	var last [windows + 1]atomic.Uint64
	stop, ended := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(stop); <-ended })
	go func() {
		defer close(ended)
		var held []*ports.ContentBatch
		giveBack := func(n int) {
			for ; n > 0 && len(held) > 0; n-- {
				b := held[0]
				held = held[1:]
				b.Reset()
				ret.Return(b)
			}
		}
		for n := 0; ; n++ {
			var b *ports.ContentBatch
			select {
			case b = <-in:
			default:
				// Nothing waiting: hand everything back, as an idle
				// output does, then block.
				giveBack(len(held))
				select {
				case b = <-in:
				case <-stop:
					return
				}
			}
			for _, c := range b.Items {
				if c.Seq > last[c.ID].Load() {
					last[c.ID].Store(c.Seq)
				}
			}
			select {
			case progress <- struct{}{}:
			default:
			}
			held = append(held, b)
			if len(held) >= 2 {
				giveBack(1 + n%2) // a burst of one or two
			}
		}
	}()
	for range rounds {
		s.publishRound(windows)
	}
	var want [windows + 1]uint64
	s.display.Do(func() {
		for id := 1; id <= windows; id++ {
			want[id] = s.contentSeq[ports.WindowID(id)]
		}
	})
	deadline := waylandDeadline(5 * time.Second)
	for {
		missing := 0
		for id := 1; id <= windows; id++ {
			if last[id].Load() != want[id] {
				missing++
			}
		}
		if missing == 0 {
			return
		}
		select {
		case <-progress:
			continue
		case <-deadline:
			for id := 1; id <= windows; id++ {
				t.Errorf("window %d: output saw seq %d, newest is %d", id, last[id].Load(), want[id])
			}
			t.Fatal("content stranded")
		}
	}
}

// publishRound publishes one content per window in one display turn.
func (s *Server) publishRound(windows int) {
	s.display.Do(func() {
		for id := 1; id <= windows; id++ {
			s.emitContent(solid(ports.WindowID(id)), damage{full: true})
		}
	})
}
