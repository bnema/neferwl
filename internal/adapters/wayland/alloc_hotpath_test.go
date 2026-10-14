package wayland

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
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
