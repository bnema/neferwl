package wayland

import (
	"context"
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
	go func() { done <- s.Run(ctx) }()
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

func nextBatch(t *testing.T, in <-chan *ports.ContentBatch) *ports.ContentBatch {
	t.Helper()
	select {
	case b := <-in:
		return b
	case <-time.After(2 * time.Second):
		t.Fatal("no batch")
		return nil
	}
}

func noBatch(t *testing.T, in <-chan *ports.ContentBatch) {
	t.Helper()
	select {
	case b := <-in:
		t.Fatalf("unexpected batch %+v", b.Items)
	case <-time.After(30 * time.Millisecond):
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
	noBatch(t, in) // nothing moves before the output returns a batch...
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
	noBatch(t, in2)
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
	s.DetachOutput("A")
	// Waiting contents of a detached link are never sent; Return is safe.
	a.Reset()
	retA.Return(a)
	s.publish(t, solid(1))
	noBatch(t, inA)
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
