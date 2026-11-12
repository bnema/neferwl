package wayland

import (
	"context"
	"fmt"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/domain"
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
	t.Cleanup(func() { cancel(); discardedErr := <-done; _ = discardedErr })
	return s
}

// route is fixture wiring: core's decision that the output called name lists
// ids (generation 0: no gate batch). Before the attach it is stored for it.
func (s *Server) route(t *testing.T, name string, ids ...domain.WindowID) {
	t.Helper()
	if !s.display.Do(func() { s.router.setRoutes([]domain.ContentRoute{{Output: name, Windows: ids}}) }) {
		t.Fatal("display stopped")
	}
}

func solid(id domain.WindowID) domain.SurfaceContent {
	return domain.SurfaceContent{ID: id, Solid: &domain.SolidColor{}}
}

func (s *Server) publish(t *testing.T, contents ...domain.SurfaceContent) {
	t.Helper()
	if !s.display.Do(func() {
		for _, c := range contents {
			s.emitContent(c, damage{full: true})
		}
	}) {
		t.Fatal("display stopped")
	}
}

//testpolicy:realtime channel fed by the real display goroutine (Server.Run on libwayland epoll)
func nextBatch(t *testing.T, in <-chan *domain.ContentBatch) *domain.ContentBatch {
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
func noBatch(t *testing.T, s *Server, in <-chan *domain.ContentBatch) {
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
	s.route(t, "A", 1, 2)
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
	s.publish(t, solid(1), solid(2), solid(3), domain.SurfaceContent{ID: 2}, solid(1))
	s.route(t, "A", 1, 2, 3)
	in, _ := s.AttachOutput("A")
	b := nextBatch(t, in)
	got := map[domain.WindowID]uint64{}
	for _, c := range b.Items {
		got[c.ID] = c.Seq
	}
	if len(got) != 2 || len(b.Items) != 2 || got[1] != 2 || got[3] != 1 {
		t.Fatalf("seed batch %+v", b.Items)
	}
	// An attach with nothing to show sends nothing.
	s.display.Do(func() { clear(s.router.latest) })
	in2, _ := s.AttachOutput("B")
	noBatch(t, s, in2)
}

// An empty content reaches the attached outputs (it releases the window's
// buffers) and removes the window from what a later output starts with.
func TestContentBatchDeliversEmpty(t *testing.T) {
	s := linkServer(t)
	s.route(t, "A", 1)
	in, ret := s.AttachOutput("A")
	s.publish(t, solid(1))
	b := nextBatch(t, in)
	b.Reset()
	ret.Return(b)
	s.publish(t, domain.SurfaceContent{ID: 1})
	b = nextBatch(t, in)
	if len(b.Items) != 1 || b.Items[0].ID != 1 || !b.Items[0].Empty() || b.Items[0].Seq != 2 {
		t.Fatalf("empty not delivered: %+v", b.Items)
	}
	var left int
	s.display.Do(func() { left = len(s.router.latest) })
	if left != 0 {
		t.Fatalf("latest keeps %d emptied windows", left)
	}
	// Superseded by an empty before the output looked: the empty wins.
	b.Reset()
	ret.Return(b)
	s.publish(t, solid(1))                               // sent
	s.publish(t, solid(1), domain.SurfaceContent{ID: 1}) // pending: last one is empty
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
	s.route(t, "A", 1)
	s.route(t, "B", 1)
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
	s.display.Do(func() { links = len(s.router.links) })
	if links != 2 {
		t.Fatalf("%d links, want 2", links)
	}
}

// Detaching a feed by its handle never removes a newer feed of the same
// name: A attached again replaced the first link, and detaching the old
// handle leaves the new one fed.
func TestDetachOldHandleKeepsReplacedLink(t *testing.T) {
	s := linkServer(t)
	s.route(t, "A", 1)
	_, oldRet := s.AttachOutput("A")
	in, _ := s.AttachOutput("A")
	s.DetachOutput(oldRet)
	var links int
	s.display.Do(func() { links = len(s.router.links) })
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
		router: newContentRouter(nil, nil, nil, nil),
	}
	s.router.setRoutes([]domain.ContentRoute{{Output: "A", Windows: []domain.WindowID{1, 2}}})
	in, ret := s.attachOutput("A")
	c := solid(1)
	other := solid(2)
	cycle := func() {
		for _, c := range []domain.SurfaceContent{c, other} {
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
//testpolicy:realtime channel fed by the real display goroutine (Server.Run on libwayland epoll)
func TestContentBatchAttachReplaysLargeSet(t *testing.T) {
	s := linkServer(t)
	const windows = 100
	all := make([]domain.SurfaceContent, 0, windows)
	for id := 1; id <= windows; id++ {
		all = append(all, solid(domain.WindowID(id)))
	}
	s.publish(t, all...)
	listed := make([]domain.WindowID, windows)
	for i := range listed {
		listed[i] = domain.WindowID(i + 1)
	}
	s.route(t, "A", listed...)
	attached := make(chan (<-chan *domain.ContentBatch), 1)
	go func() { in, _ := s.AttachOutput("A"); attached <- in }()
	var in <-chan *domain.ContentBatch
	select {
	case in = <-attached:
	case <-waylandDeadline(2 * time.Second):
		t.Fatal("AttachOutput waits for the output to read")
	}
	b := nextBatch(t, in)
	ids := map[domain.WindowID]struct{}{}
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
//testpolicy:realtime channel fed by the real display goroutine (Server.Run on libwayland epoll)
func TestContentBatchHandshakeNeverStrandsContent(t *testing.T) {
	s := linkServer(t)
	s.route(t, "A", 1, 2, 3, 4)
	in, ret := s.AttachOutput("A")
	const windows, rounds = 4, 3000
	progress := make(chan struct{}, 1)
	var last [windows + 1]atomic.Uint64
	stop, ended := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(stop); <-ended })
	go func() {
		defer close(ended)
		var held []*domain.ContentBatch
		giveBack := func(n int) {
			for ; n > 0 && len(held) > 0; n-- {
				b := held[0]
				held = held[1:]
				b.Reset()
				ret.Return(b)
			}
		}
		for n := 0; ; n++ {
			var b *domain.ContentBatch
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
			want[id] = s.router.contentSeq[domain.WindowID(id)]
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
			s.emitContent(solid(domain.WindowID(id)), damage{full: true})
		}
	})
}

func TestContentRouterSlotLimitAndReuse(t *testing.T) {
	s := &Server{}
	s.router = newContentRouter(s, s.release, s.bufferReferenced, func(string) bool { return true })
	r := &s.router
	var handles []ports.ContentReturn
	for i := range 64 {
		in, ret := r.attach(fmt.Sprintf("output-%d", i))
		require.NotNil(t, in)
		require.NotNil(t, ret)
		require.Equal(t, uint(i), ret.(*contentLink).slot)
		handles = append(handles, ret)
	}
	in, ret := r.attach("overflow")
	require.Nil(t, in)
	require.Nil(t, ret)
	routes := make([]domain.ContentRoute, 0, 64)
	for i := range 64 {
		routes = append(routes, domain.ContentRoute{Output: fmt.Sprintf("output-%d", i), Windows: []domain.WindowID{1}})
	}
	r.setRoutes(routes)
	r.publish(solid(1), damage{})
	require.Equal(t, ^linkMask(0), r.delivered[1])
	now := time.Unix(100, 0)
	r.hold(heldBuffer{window: 1, at: now})
	// Isolate a hold targeting the old slot; every other recipient has advanced.
	r.held[0].targets = linkMask(1) << 17
	r.detach(handles[17])
	require.Zero(t, r.delivered[1]&(linkMask(1)<<17))
	require.Zero(t, r.held[0].targets)
	_, replacement := r.attach("replacement")
	require.Equal(t, uint(17), replacement.(*contentLink).slot)
	require.Zero(t, r.held[0].targets, "replay cannot retroactively target a held buffer")
	require.False(t, r.settle(now, nil), "old target cannot survive slot reuse")
	r.detach(handles[17])
	require.Len(t, r.links, 64, "stale handle must not detach replacement")
}

// routerRig is a content router with no display: batches are taken inline,
// like an output that keeps up.
type routerRig struct {
	t   *testing.T
	r   *contentRouter
	in  map[string]<-chan *domain.ContentBatch
	ret map[string]ports.ContentReturn
}

func newRouterRig(t *testing.T) *routerRig {
	t.Helper()
	s := &Server{}
	s.router = newContentRouter(s, s.release, s.bufferReferenced, func(string) bool { return true })
	return &routerRig{t: t, r: &s.router, in: map[string]<-chan *domain.ContentBatch{}, ret: map[string]ports.ContentReturn{}}
}

func (g *routerRig) attach(name string) {
	g.t.Helper()
	in, ret := g.r.attach(name)
	require.NotNil(g.t, in)
	g.in[name], g.ret[name] = in, ret
}

func (g *routerRig) route(name string, gen uint64, ids ...domain.WindowID) {
	g.r.setRoutes([]domain.ContentRoute{{Output: name, Gen: gen, Windows: ids}})
}

// take returns what the output has been sent (nil: no batch), after giving
// the batch back.
func (g *routerRig) take(name string) *domain.ContentBatch {
	g.t.Helper()
	select {
	case b := <-g.in[name]:
		got := &domain.ContentBatch{Items: append([]domain.SurfaceContent(nil), b.Items...), Routes: b.Routes}
		b.Reset()
		g.ret[name].Return(b)
		return got
	default:
		return nil
	}
}

func itemSeqs(b *domain.ContentBatch) map[domain.WindowID]uint64 {
	got := map[domain.WindowID]uint64{}
	if b == nil {
		return got
	}
	for _, c := range b.Items {
		got[c.ID] = c.Seq
	}
	return got
}

func TestContentRoutedOnlyToListingLinks(t *testing.T) {
	g := newRouterRig(t)
	g.attach("A")
	g.attach("B")
	g.route("A", 1, 1)
	g.route("B", 1, 2)
	g.take("A")
	g.take("B") // generation batches
	g.r.publish(solid(1), damage{full: true})
	g.r.publish(solid(2), damage{full: true})
	require.Equal(t, map[domain.WindowID]uint64{1: 1}, itemSeqs(g.take("A")))
	require.Equal(t, map[domain.WindowID]uint64{2: 1}, itemSeqs(g.take("B")))
}

func TestContentAttachBeforeAnyRouteDeliversNothing(t *testing.T) {
	g := newRouterRig(t)
	g.r.publish(solid(1), damage{full: true})
	g.attach("A")
	require.Nil(t, g.take("A"))
	g.r.publish(solid(1), damage{full: true})
	require.Nil(t, g.take("A"), "still no route")
	g.route("A", 1, 1)
	b := g.take("A")
	require.Equal(t, map[domain.WindowID]uint64{1: 2}, itemSeqs(b))
	require.Equal(t, uint64(1), b.Routes)
}

func TestContentJoinResendsLatestWithGeneration(t *testing.T) {
	g := newRouterRig(t)
	g.attach("A")
	g.attach("B")
	g.route("A", 1, 1)
	g.take("A")
	g.r.publish(solid(1), damage{full: true})
	g.take("A")
	require.Nil(t, g.take("B"))
	g.route("B", 1, 1)
	b := g.take("B")
	require.Equal(t, map[domain.WindowID]uint64{1: 1}, itemSeqs(b))
	require.Equal(t, uint64(1), b.Routes)
	require.Equal(t, linkMask(3), g.r.delivered[1])
}

func TestContentGenerationWithoutJoinYieldsEmptyBatch(t *testing.T) {
	g := newRouterRig(t)
	g.attach("A")
	g.route("A", 3, 7) // window 7 has no content yet
	b := g.take("A")
	require.NotNil(t, b)
	require.Empty(t, b.Items)
	require.Equal(t, uint64(3), b.Routes)
	g.route("A", 3, 7, 8) // same generation: nothing new to say
	require.Nil(t, g.take("A"))
	g.route("A", 4, 7, 8, 9)
	b = g.take("A")
	require.NotNil(t, b)
	require.Equal(t, uint64(4), b.Routes)
}

func TestContentLazyLeaveWithdrawsAtNextContent(t *testing.T) {
	g := newRouterRig(t)
	g.attach("A")
	g.attach("B")
	g.route("A", 1, 1)
	g.take("A")
	g.r.publish(solid(1), damage{full: true})
	g.take("A")
	g.route("B", 1, 1) // window 1 moves to B ...
	g.take("B")
	g.route("A", 1) // ... and leaves A: nothing is sent at once
	require.Nil(t, g.take("A"))
	require.Equal(t, linkMask(3), g.r.delivered[1])
	c := solid(1)
	c.Seq = 0
	g.r.publish(c, damage{full: true})
	a, b := g.take("A"), g.take("B")
	require.Len(t, a.Items, 1)
	require.True(t, a.Items[0].Empty(), "the leaver gets a withdraw")
	require.Equal(t, uint64(2), a.Items[0].Seq, "with the Seq of the real content")
	require.Len(t, b.Items, 1)
	require.False(t, b.Items[0].Empty())
	require.Equal(t, uint64(2), b.Items[0].Seq)
	require.Equal(t, linkMask(2), g.r.delivered[1])
	g.r.publish(solid(1), damage{full: true})
	require.Nil(t, g.take("A"), "a link that left and withdrew hears nothing more")
}

func TestContentRejoinBeforeNextCommitSendsNoItem(t *testing.T) {
	g := newRouterRig(t)
	g.attach("A")
	g.route("A", 1, 1)
	g.take("A")
	g.r.publish(solid(1), damage{full: true})
	g.take("A")
	g.route("A", 1)
	g.route("A", 2, 1)
	b := g.take("A")
	require.NotNil(t, b, "the generation still goes")
	require.Empty(t, b.Items)
	require.Equal(t, uint64(2), b.Routes)
}

func TestContentUnmapReachesPendingLeaver(t *testing.T) {
	g := newRouterRig(t)
	g.attach("A")
	g.attach("B")
	g.route("A", 1, 1)
	g.route("B", 1, 1)
	g.take("A")
	g.take("B")
	g.r.publish(solid(1), damage{full: true})
	g.take("A")
	g.take("B")
	g.route("A", 1) // pending leaver
	g.take("A")
	g.r.publish(domain.SurfaceContent{ID: 1}, damage{full: true})
	for _, name := range []string{"A", "B"} {
		b := g.take(name)
		require.Len(t, b.Items, 1, name)
		require.True(t, b.Items[0].Empty(), name)
		require.Equal(t, uint64(2), b.Items[0].Seq, name)
	}
	require.NotContains(t, g.r.delivered, domain.WindowID(1))
}

// A buffer replaced while a leave is pending is released only after the
// leaver saw the withdraw, or after heldTimeout.
func TestContentBufferReplacedDuringPendingLeave(t *testing.T) {
	g := newRouterRig(t)
	g.attach("A")
	g.attach("B")
	g.route("A", 1, 1)
	g.route("B", 1, 1)
	g.r.publish(solid(1), damage{full: true})
	g.route("A", 2) // A leaves lazily
	now := time.Unix(100, 0)
	// The buffer is replaced: targets are read before the replacing content.
	g.r.hold(heldBuffer{window: 1, after: g.r.seq(1), at: now})
	require.Equal(t, linkMask(3), g.r.held[0].targets, "includes the pending leaver")
	g.r.publish(solid(1), damage{full: true}) // Seq 2: real content on B, withdraw on A
	seen := func(seq uint64) []ports.OutputPresented {
		m := map[domain.WindowID]uint64{1: seq}
		return []ports.OutputPresented{{Output: "A", Seen: m}, {Output: "B", Seen: m}}
	}
	require.True(t, g.r.settle(now, seen(1)), "neither saw a later content")
	onlyB := []ports.OutputPresented{{Output: "A", Seen: map[domain.WindowID]uint64{1: 1}}, {Output: "B", Seen: map[domain.WindowID]uint64{1: 2}}}
	require.True(t, g.r.settle(now, onlyB), "the leaver has not seen the withdraw")
	require.False(t, g.r.settle(now, seen(2)), "both saw a later content")

	g.r.hold(heldBuffer{window: 1, after: g.r.seq(1), at: now})
	require.True(t, g.r.settle(now.Add(heldTimeout-time.Millisecond), onlyB))
	require.False(t, g.r.settle(now.Add(heldTimeout), onlyB), "heldTimeout releases")
}

func TestContentLeaverQueuedHoldsPastTimeout(t *testing.T) {
	g := newRouterRig(t)
	g.attach("A")
	g.route("A", 1, 1)
	g.r.publish(solid(1), damage{full: true})
	g.route("A", 2)
	now := time.Unix(100, 0)
	g.r.hold(heldBuffer{window: 1, id: 9, after: g.r.seq(1), at: now})
	g.r.publish(solid(1), damage{full: true})
	queued := []ports.OutputPresented{{Output: "A", Queued: 9, Seen: map[domain.WindowID]uint64{1: 2}}}
	require.True(t, g.r.settle(now.Add(time.Hour), queued), "a queued buffer holds with no timeout")
	require.False(t, g.r.settle(now.Add(time.Hour), []ports.OutputPresented{{Output: "A", Seen: map[domain.WindowID]uint64{1: 2}}}))
}

// Routing a content, a join and a generation allocate nothing once the
// batches and maps have their capacity: the route lists are reused by value.
func TestContentRoutedPublishAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector allocates in the background")
	}
	g := newRouterRig(t)
	g.attach("A")
	g.attach("B")
	toA := []domain.ContentRoute{{Output: "A", Gen: 1, Windows: []domain.WindowID{1, 2}}}
	toB := []domain.ContentRoute{{Output: "B", Gen: 1, Windows: []domain.WindowID{1, 2}}}
	g.r.setRoutes(toA)
	g.r.setRoutes(toB)
	drain := func() {
		for _, name := range []string{"A", "B"} {
			select {
			case b := <-g.in[name]:
				b.Reset()
				g.ret[name].Return(b)
			default:
			}
		}
	}
	c1, c2 := solid(1), solid(2)
	cycle := func() {
		g.r.publish(c1, damage{full: true})
		drain()
		g.r.publish(c2, damage{full: true})
		drain()
	}
	for range 3 * damageHistoryCap {
		cycle()
	}
	if allocs := testing.AllocsPerRun(200, cycle); allocs != 0 {
		t.Errorf("routed publish cycle: %.2f allocs, want 0", allocs)
	}
}
