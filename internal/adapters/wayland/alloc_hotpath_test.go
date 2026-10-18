package wayland

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// The steady-state hot paths that run for every frame or commit allocate
// nothing (or one pooled object per damage-history generation).

func TestReleaseHeldAllocations(t *testing.T) {
	out := &output{place: ports.OutputPlacement{Info: ports.OutputInfo{Name: "DP-2", RefreshMilli: 60000}}}
	s := &Server{outputs: []*output{out}, reports: map[string]ports.OutputPresented{}}
	reports := []ports.OutputPresented{{Output: "DP-2", Seen: map[ports.WindowID]uint64{1: 1}}}
	now := time.Unix(100, 0)
	s.releaseHeld(now, reports)
	if allocs := testing.AllocsPerRun(100, func() { s.releaseHeld(now, reports) }); allocs != 0 {
		t.Errorf("releaseHeld: %.1f allocs, want 0", allocs)
	}
}

func TestCommitDamageAllocations(t *testing.T) {
	if raceEnabled {
		// AllocsPerRun counts every goroutine, and the race runtime
		// allocates on its own now and then. perf-check runs it without.
		t.Skip("the race detector allocates in the background")
	}
	srv := &Server{fifoSurfaces: map[*surface]struct{}{}}
	surf := &surface{server: srv}
	commit := func() {
		surf.Damage(nil, 0, 0, 4, 4)
		surf.DamageBuffer(nil, 1, 1, 2, 2)
		surf.queueUpdate()
		surf.queue[0].applyGraph()
	}
	commit()
	if allocs := testing.AllocsPerRun(100, commit); allocs != 0 {
		t.Errorf("damage requests + queueUpdate + apply: %.1f allocs, want 0", allocs)
	}
	// The queued update keeps its rects while the next commit collects its own.
	surf.Damage(nil, 0, 0, 1, 1)
	surf.queueUpdate()
	first := surf.queue[0]
	surf.Damage(nil, 5, 5, 2, 2)
	if len(first.damage) != 1 || first.damage[0].W != 1 || len(surf.next.damage) != 1 || surf.next.damage[0].W != 2 {
		t.Fatalf("damage shared between commits: queued %v pending %v", first.damage, surf.next.damage)
	}
	surf.queueUpdate()
	if second := surf.queue[1]; len(second.damage) != 1 || second.damage[0].W != 2 || first.damage[0].W != 1 {
		t.Fatalf("damage mixed up: %v %v", first.damage, second.damage)
	}
}

// An explicit-sync commit moves its points into the update: the commit
// itself allocates no commitSync, and a pending one is not overwritten.
func TestCommitSyncStorageIsPerUpdate(t *testing.T) {
	dev := newMocksyncobjDevice(t)
	dev.EXPECT().signalled(mock.Anything, mock.Anything).Return(true, nil)
	srv := &Server{fifoSurfaces: map[*surface]struct{}{}}
	sw, err := newSyncWaiter(func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer sw.discard()
	defer sw.close()
	srv.syncWait = sw
	tl := &timeline{dev: dev, handle: 7, alive: true}
	surf := &surface{server: srv, sync: &syncState{}}
	take := func(point uint64) {
		surf.sync.acquire, surf.sync.release = syncPoint{tl: tl, point: point}, syncPoint{tl: tl, point: point + 1}
		surf.takeSyncPoints()
		surf.queueUpdate()
	}
	take(10)
	take(20)
	a, b := surf.queue[0], surf.queue[1]
	if a.sync == nil || b.sync == nil || a.sync == b.sync || a.sync.acquire.point != 10 || b.sync.acquire.point != 20 || surf.next.sync != nil {
		t.Fatalf("sync points mixed up: %+v %+v pending %+v", a.sync, b.sync, surf.next.sync)
	}
	// The commit's points live in the surface until queueUpdate: no heap
	// commitSync per commit (the mocked device's own allocations would drown
	// an AllocsPerRun count here).
	surf.sync.acquire, surf.sync.release = syncPoint{tl: tl, point: 30}, syncPoint{tl: tl, point: 31}
	surf.takeSyncPoints()
	if surf.next.sync != &surf.syncScratch {
		t.Fatal("commit points not held in the surface scratch")
	}
}

func TestEmitContentAllocations(t *testing.T) {
	s := &Server{
		contentSeq: map[ports.WindowID]uint64{},
		damage:     map[ports.WindowID][]ports.SeqDamage{},
		latest:     map[ports.WindowID]ports.SurfaceContent{},
	}
	c := ports.SurfaceContent{ID: 1, Solid: &ports.SolidColor{}}
	var held []ports.SurfaceContent // readers retaining published histories
	var snapshots [][]ports.SeqDamage
	for range 3 * damageHistoryCap {
		s.emitContent(c, damage{full: true})
		got := s.latest[1]
		held = append(held, got)
		snapshots = append(snapshots, append([]ports.SeqDamage(nil), got.DamageHistory...))
	}
	for i, h := range held {
		// A published history never changes, nor can its owner append to it.
		if len(h.DamageHistory) != len(snapshots[i]) || cap(h.DamageHistory) != len(h.DamageHistory) {
			t.Fatalf("history %d: len %d cap %d", i, len(h.DamageHistory), cap(h.DamageHistory))
		}
		for j, d := range h.DamageHistory {
			if d.Seq != snapshots[i][j].Seq || d.Full != snapshots[i][j].Full {
				t.Fatalf("history %d changed at %d: %+v want %+v", i, j, d, snapshots[i][j])
			}
		}
		if n := len(h.DamageHistory); h.DamageHistory[n-1].Seq != h.Seq || n > damageHistory {
			t.Fatalf("history %d: %+v", i, h.DamageHistory)
		}
	}
	allocs := testing.AllocsPerRun(100, func() { s.emitContent(c, damage{full: true}) })
	if allocs != 0 {
		t.Errorf("emitContent: %.2f allocs, want 0 (amortized)", allocs)
	}
	t.Logf("emitContent: %.2f allocs", allocs)
}

// Firing an output's callbacks keeps its queue's backing array, so the next
// commit's queueFrames appends in place (before: the queue was deleted by
// every fire and each commit grew a fresh slice). An emptied queue reads
// like an absent one, and pins no fired callback.
func TestFrameQueueKeepsBackingArray(t *testing.T) {
	s, events, commands, _, _, dir := presentServerOn(t, testOutputs)
	c := protocolClient(t, s, dir)
	w, surf, _ := surfaceMapper(t, c, events)()
	placeWindow(t, s, commands, w, "HEADLESS-1")
	awaitFrame(t, c, requestFrame(t, c, surf))
	awaitFrame(t, c, requestFrame(t, c, surf))
	var first *wayland.Callback
	var head **wayland.Callback
	s.display.Do(func() {
		// The pacer fired the queue during the awaitFrame calls above.
		queue := s.awaiting["HEADLESS-1"]
		if len(queue) != 0 || cap(queue) == 0 {
			t.Errorf("fired queue: len %d cap %d", len(queue), cap(queue))
			return
		}
		first, head = queue[:1][0], &queue[:1][0]
	})
	if first != nil {
		t.Fatal("fired queue pins a callback")
	}
	awaitFrame(t, c, requestFrame(t, c, surf))
	s.display.Do(func() {
		if again := s.awaiting["HEADLESS-1"]; cap(again) == 0 || &again[:1][0] != head {
			t.Error("queue reallocated between frames")
		}
	})
}

// dueFrames hands out one scratch slice for every tick.
func TestDueFramesFireScratch(t *testing.T) {
	out := &output{place: ports.OutputPlacement{Info: ports.OutputInfo{Name: "DP-2", RefreshMilli: 60000}}}
	s := &Server{outputs: []*output{out}, frameDue: map[string]time.Time{}, awaiting: map[string][]*wayland.Callback{"DP-2": {nil}}}
	now := time.Unix(100, 0)
	flipped := map[string]bool{"DP-2": true}
	fire, _, _ := s.dueFrames(now, flipped)
	if !slices.Equal(fire, []string{"DP-2"}) {
		t.Fatalf("fire %v", fire)
	}
	if allocs := testing.AllocsPerRun(100, func() { s.dueFrames(now, flipped) }); allocs != 0 {
		t.Errorf("firing dueFrames: %.1f allocs, want 0", allocs)
	}
}

// A buffer sends the same SHMBuffer on every commit and still checks the
// file's size each time: a client may truncate it.
func TestBufferContentAllocations(t *testing.T) {
	fd, err := unix.MemfdCreate("content-allocs", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(fd), "content-allocs")
	defer f.Close()
	if err := f.Truncate(28); err != nil {
		t.Fatal(err)
	}
	b := &buffer{pool: &pool{id: 3, file: f, size: 28}, offset: 4, width: 2, height: 2, stride: 16}
	first, ok := b.content(1)
	if !ok {
		t.Fatal("no content")
	}
	if allocs := testing.AllocsPerRun(100, func() { b.content(1) }); allocs != 0 {
		t.Errorf("buffer.content: %.1f allocs, want 0", allocs)
	}
	if again, _ := b.content(2); again.SHM != first.SHM {
		t.Fatal("SHMBuffer rebuilt per commit")
	}
	if err := f.Truncate(27); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.content(1); ok {
		t.Fatal("content from a truncated pool")
	}
}

// Forwarding a command allocates nothing on the push path: queueing a
// command, folding a motion into the queued one and draining the ring. (Only
// applying a merged motion boxes the summed value again, once per merged run,
// as the old batch coalescing did once per merge; that is excluded here.)
func TestCommandForwardAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector makes the purego wake callback allocate")
	}
	s, _, _, _, _, _ := presentServer(t)
	r := newCommandRing(s.display, func(queuedCommand) {})
	var button ports.ClientCommand = ports.PointerButtonTo{ID: 1, Button: 272, Pressed: true}
	var motion ports.ClientCommand = ports.PointerMotionTo{ID: 1, DX: 1}
	state := ports.SecurityState{Generation: 2, Protected: true}
	var wrapped ports.ClientCommand = ports.SecurityCommand{State: state, Command: ports.PointerMotionTo{ID: 1, DX: 1}}
	ran := make(chan struct{}, 1)
	marker := func() { ran <- struct{}{} }
	cycle := func() {
		r.Push(queue(button))
		r.Push(queue(motion))
		r.Push(queue(motion)) // folded into the previous one
		r.Push(queue(wrapped))
		r.Push(queue(wrapped)) // enveloped, same state: folded as well
		s.display.Post(marker)
		<-ran
	}
	for range 4 {
		cycle()
	}
	if allocs := testing.AllocsPerRun(200, cycle); allocs != 0 {
		t.Errorf("command push + merge + drain: %.1f allocs, want 0", allocs)
	}
}
