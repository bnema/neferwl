package presented

import (
	"reflect"
	"testing"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func ptr(m map[ports.WindowID]uint64) uintptr { return reflect.ValueOf(m).Pointer() }

// An unchanged publication reuses its immutable snapshots; a change clones,
// and earlier reports keep their maps.
func TestQueueSnapshotsCloneOnlyOnChange(t *testing.T) {
	var q Queue
	seen := map[ports.WindowID]uint64{1: 4, 5: 7}
	reads := map[ports.WindowID]uint64{5: 2}
	q.Push(ports.OutputPresented{Output: "A", Seen: seen, ChildReads: reads})
	first := q.Unsent()[0]
	require.Equal(t, seen, first.Seen)
	require.Equal(t, reads, first.ChildReads)
	seen[1] = 99 // the caller's scratch changes: the report does not
	require.Equal(t, uint64(4), first.Seen[1])

	seen[1] = 4
	q.Push(ports.OutputPresented{Output: "A", Seen: seen, ChildReads: reads})
	again := q.Unsent()[0]
	require.Equal(t, ptr(first.Seen), ptr(again.Seen), "unchanged seen is shared")
	require.Equal(t, ptr(first.ChildReads), ptr(again.ChildReads), "unchanged reads are shared")
	allocs := testing.AllocsPerRun(100, func() { q.Push(ports.OutputPresented{Output: "A", Seen: seen, ChildReads: reads}) })
	require.Zero(t, allocs, "unchanged publication allocates nothing")

	reads[5] = 3
	q.Push(ports.OutputPresented{Output: "A", Seen: seen, ChildReads: reads})
	changed := q.Unsent()[0]
	require.Equal(t, map[ports.WindowID]uint64{5: 3}, changed.ChildReads)
	require.Equal(t, map[ports.WindowID]uint64{5: 2}, first.ChildReads, "sent snapshot stays immutable")
	require.Equal(t, ptr(first.Seen), ptr(changed.Seen), "seen still unchanged")
}

// Flip-less reports fold into the newest one and keep its flip; flips queue.
func TestQueueKeepsEveryFlip(t *testing.T) {
	var q Queue
	seen := map[ports.WindowID]uint64{1: 1}
	q.Push(ports.OutputPresented{Flip: &ports.FlipInfo{Seq: 1}, Seen: seen})
	q.Push(ports.OutputPresented{Seen: seen})
	q.Push(ports.OutputPresented{Flip: &ports.FlipInfo{Seq: 2}, Seen: seen})
	q.Push(ports.OutputPresented{Seen: map[ports.WindowID]uint64{1: 3}})
	u := q.Unsent()
	require.Len(t, u, 2)
	require.Equal(t, uint64(1), u[0].Flip.Seq)
	require.Equal(t, uint64(2), u[1].Flip.Seq, "a flip-less report keeps the queued flip")
	require.Equal(t, uint64(3), u[1].Seen[1])
}

// Past MaxUnsent the oldest flip folds into the next: counted as merged and
// its shown windows carried, without mutating a shared Shows snapshot.
func TestQueueMergesOldestFlipPastBound(t *testing.T) {
	var q Queue
	shared := map[ports.WindowID]uint64{2: 5}
	q.Push(ports.OutputPresented{Flip: &ports.FlipInfo{Seq: 1, Shows: map[ports.WindowID]uint64{1: 4}}})
	q.Push(ports.OutputPresented{Flip: &ports.FlipInfo{Seq: 2, Shows: shared}})
	merged := false
	for i := range MaxUnsent - 1 {
		merged = q.Push(ports.OutputPresented{Flip: &ports.FlipInfo{Seq: uint64(10 + i)}}) || merged
	}
	require.True(t, merged)
	u := q.Unsent()
	require.Len(t, u, MaxUnsent)
	require.Equal(t, uint64(2), u[0].Flip.Seq)
	require.Equal(t, 1, u[0].Flip.Merged)
	require.Equal(t, map[ports.WindowID]uint64{1: 4, 2: 5}, u[0].Flip.Shows)
	require.Equal(t, map[ports.WindowID]uint64{2: 5}, shared, "shared snapshot untouched")
}

func TestQueueFlushPushesInOrderAndEmpties(t *testing.T) {
	var q Queue
	q.Push(ports.OutputPresented{Flip: &ports.FlipInfo{Seq: 1}})
	q.Push(ports.OutputPresented{Flip: &ports.FlipInfo{Seq: 2}})
	var got []ports.OutputPresented
	sink := portsmocks.NewMockPresentationSink(t)
	sink.EXPECT().Push(mock.Anything).RunAndReturn(func(r ports.OutputPresented) { got = append(got, r) })
	q.Flush(sink)
	require.Zero(t, q.Len())
	require.Len(t, got, 2)
	require.Equal(t, uint64(1), got[0].Flip.Seq)
	require.Equal(t, uint64(2), got[1].Flip.Seq)
	q.Flush(sink) // empty: nothing more is pushed
	require.Len(t, got, 2)

	q.Push(ports.OutputPresented{Flip: &ports.FlipInfo{Seq: 3}})
	q.Reset()
	require.Zero(t, q.Len())
}
