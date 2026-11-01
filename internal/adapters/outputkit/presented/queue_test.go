package presented

import (
	"maps"
	"reflect"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func ptr(m map[domain.WindowID]uint64) uintptr { return reflect.ValueOf(m).Pointer() }

// An unchanged publication reuses its immutable snapshots; a change clones,
// and earlier reports keep their maps.
func TestQueueSnapshotsCloneOnlyOnChange(t *testing.T) {
	var q Queue
	seen := map[domain.WindowID]uint64{1: 4, 5: 7}
	reads := map[domain.WindowID]uint64{5: 2}
	q.Push(domain.OutputPresented{Output: "A", Seen: seen, ChildReads: reads})
	first := q.Unsent()[0]
	require.Equal(t, seen, first.Seen)
	require.Equal(t, reads, first.ChildReads)
	seen[1] = 99 // the caller's scratch changes: the report does not
	require.Equal(t, uint64(4), first.Seen[1])

	seen[1] = 4
	q.Push(domain.OutputPresented{Output: "A", Seen: seen, ChildReads: reads})
	again := q.Unsent()[0]
	require.Equal(t, ptr(first.Seen), ptr(again.Seen), "unchanged seen is shared")
	require.Equal(t, ptr(first.ChildReads), ptr(again.ChildReads), "unchanged reads are shared")
	allocs := testing.AllocsPerRun(100, func() { q.Push(domain.OutputPresented{Output: "A", Seen: seen, ChildReads: reads}) })
	require.Zero(t, allocs, "unchanged publication allocates nothing")

	reads[5] = 3
	q.Push(domain.OutputPresented{Output: "A", Seen: seen, ChildReads: reads})
	changed := q.Unsent()[0]
	require.Equal(t, map[domain.WindowID]uint64{5: 3}, changed.ChildReads)
	require.Equal(t, map[domain.WindowID]uint64{5: 2}, first.ChildReads, "sent snapshot stays immutable")
	require.Equal(t, first.Seen, changed.Seen, "seen still unchanged")
}

// Flip-less reports fold into the newest one and keep its flip; flips queue.
func TestQueueKeepsEveryFlip(t *testing.T) {
	var q Queue
	seen := map[domain.WindowID]uint64{1: 1}
	q.Push(domain.OutputPresented{Flip: &domain.FlipInfo{Seq: 1}, Seen: seen})
	q.Push(domain.OutputPresented{Seen: seen})
	q.Push(domain.OutputPresented{Flip: &domain.FlipInfo{Seq: 2}, Seen: seen})
	q.Push(domain.OutputPresented{Seen: map[domain.WindowID]uint64{1: 3}})
	u := q.Unsent()
	require.Len(t, u, 2)
	require.Equal(t, uint64(1), u[0].Flip.Seq)
	require.Equal(t, uint64(2), u[1].Flip.Seq, "a flip-less report keeps the queued flip")
	require.Equal(t, uint64(3), u[1].Seen[1])
}

// Merge folds a flip into the next: counted as merged and its shown windows
// carried, without mutating a shared Shows snapshot.
func TestMergeCarriesShowsWithoutMutatingSharedSnapshot(t *testing.T) {
	shared := map[domain.WindowID]uint64{2: 5}
	old := &domain.FlipInfo{Seq: 1, Merged: 2, Shows: map[domain.WindowID]uint64{1: 4}, ScanoutReason: "nomem", OverlayReason: "no_plane"}
	next := &domain.FlipInfo{Seq: 2, Shows: shared, ScanoutReason: "flip_refused"}
	Merge(old, next)
	require.Equal(t, "flip_refused", next.ScanoutReason, "newest reasons kept")
	require.Empty(t, next.OverlayReason)
	require.Equal(t, 3, next.Merged)
	require.Equal(t, map[domain.WindowID]uint64{1: 4, 2: 5}, next.Shows)
	require.Equal(t, map[domain.WindowID]uint64{2: 5}, shared, "shared snapshot untouched")
}

// The queue never merges flips: every flip of a turn is pushed to the sink.
func TestQueueDoesNotBoundFlips(t *testing.T) {
	var q Queue
	for i := range 40 {
		q.Push(domain.OutputPresented{Flip: &domain.FlipInfo{Seq: uint64(i + 1)}})
	}
	u := q.Unsent()
	require.Len(t, u, 40)
	require.Zero(t, u[0].Flip.Merged)
	require.Equal(t, uint64(1), u[0].Flip.Seq)
}

func TestQueueFlushPushesInOrderAndEmpties(t *testing.T) {
	var q Queue
	q.Push(domain.OutputPresented{Flip: &domain.FlipInfo{Seq: 1}})
	q.Push(domain.OutputPresented{Flip: &domain.FlipInfo{Seq: 2}})
	var got []domain.OutputPresented
	sink := portsmocks.NewMockPresentationSink(t)
	sink.EXPECT().Push(mock.Anything).RunAndReturn(func(r domain.OutputPresented) { got = append(got, r) })
	q.Flush(sink)
	require.Zero(t, q.Len())
	require.Len(t, got, 2)
	require.Equal(t, uint64(1), got[0].Flip.Seq)
	require.Equal(t, uint64(2), got[1].Flip.Seq)
	q.Flush(sink) // empty: nothing more is pushed
	require.Len(t, got, 2)

	q.Push(domain.OutputPresented{Flip: &domain.FlipInfo{Seq: 3}})
	q.Reset()
	require.Zero(t, q.Len())
}

// Changed sequences exercise the live flip path, including consumer returns.
func TestPresentationSnapshotAllocations(t *testing.T) {
	var q Queue
	seen := make(map[domain.WindowID]uint64, 200)
	reads := make(map[domain.WindowID]uint64, 200)
	for id := domain.WindowID(1); id <= 200; id++ {
		seen[id] = 1
		reads[id] = 1
	}
	var baseline domain.OutputPresented
	before := testing.AllocsPerRun(1000, func() {
		for id := range seen {
			seen[id]++
			reads[id]++
		}
		baseline.Seen, baseline.ChildReads = maps.Clone(seen), maps.Clone(reads)
	})
	require.Equal(t, seen, baseline.Seen)
	t.Logf("previous clone path: %.1f allocs/push", before)
	cycle := func() {
		for id := range seen {
			seen[id]++
			reads[id]++
		}
		q.Push(domain.OutputPresented{Seen: seen, ChildReads: reads})
		r := q.Unsent()[0]
		r.Snapshot.Retain() // the consumer owns this reference after Reset
		q.Reset()
		r.Snapshot.Release()
	}
	for range 10 {
		cycle()
	}
	allocs := testing.AllocsPerRun(1000, cycle)
	t.Logf("changing Seen and ChildReads: %.1f allocs/push + release", allocs)
	require.Zero(t, allocs)
}

func TestSentSnapshotSurvivesLaterPushes(t *testing.T) {
	var q Queue
	seen := map[domain.WindowID]uint64{1: 1}
	reads := map[domain.WindowID]uint64{1: 2}
	sink := portsmocks.NewMockPresentationSink(t)
	var sent domain.OutputPresented
	sink.EXPECT().Push(mock.Anything).RunAndReturn(func(r domain.OutputPresented) { sent = r }).Once()
	q.Push(domain.OutputPresented{Seen: seen, ChildReads: reads})
	q.Flush(sink)
	for range 100 {
		seen[1]++
		reads[1]++
		q.Push(domain.OutputPresented{Seen: seen, ChildReads: reads})
		q.Reset()
	}
	require.Equal(t, uint64(1), sent.Seen[1])
	require.Equal(t, uint64(2), sent.ChildReads[1])
	sent.Snapshot.Release()
}

func TestSentSnapshotConcurrentReader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var q Queue
		seen := map[domain.WindowID]uint64{1: 1}
		reads := map[domain.WindowID]uint64{1: 2}
		sink := portsmocks.NewMockPresentationSink(t)
		reports := make(chan domain.OutputPresented, 1)
		sink.EXPECT().Push(mock.Anything).RunAndReturn(func(r domain.OutputPresented) { reports <- r }).Once()
		q.Push(domain.OutputPresented{Seen: seen, ChildReads: reads})
		q.Flush(sink)
		done := make(chan struct{})
		go func() {
			defer close(done)
			r := <-reports
			defer r.Snapshot.Release()
			for range 1000 {
				if r.Seen[1] != 1 || r.ChildReads[1] != 2 {
					t.Error("sent maps mutated")
					return
				}
			}
		}()
		for range 1000 {
			seen[1]++
			reads[1]++
			q.Push(domain.OutputPresented{Seen: seen, ChildReads: reads})
			q.Reset()
		}
		<-done
	})
}
