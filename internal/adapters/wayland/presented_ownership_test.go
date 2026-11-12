package wayland

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/outputkit/presented"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestPresentationRingEvictionReleasesSnapshot(t *testing.T) {
	returned := make(chan *ports.PresentationSnapshot, 1)
	snapshot := &ports.PresentationSnapshot{Seen: map[domain.WindowID]uint64{1: 4}}
	snapshot.Init(returned)
	p := &presentSink{ring: []ports.OutputPresented{{Output: "A", Seen: snapshot.Seen, Snapshot: snapshot}}}
	for len(p.ring) < maxPresentRing {
		p.ring = append(p.ring, ports.OutputPresented{Output: "A"})
	}
	incoming := ports.OutputPresented{Output: "A"}
	require.False(t, p.makeRoomLocked(&incoming))
	require.Len(t, p.ring, maxPresentRing-1)
	require.Len(t, returned, 1)
	require.Same(t, snapshot, <-returned)
	require.Nil(t, incoming.Snapshot, "eviction must not transfer the old map lease")
}

func TestLatestOutputReplacementAndRemovalReleaseSnapshots(t *testing.T) {
	returned := make(chan *ports.PresentationSnapshot, 2)
	first, next := &ports.PresentationSnapshot{}, &ports.PresentationSnapshot{}
	first.Init(returned)
	next.Init(returned)
	out := &output{place: domain.OutputPlacement{Info: domain.OutputInfo{Name: "DP-2"}}}
	s := &Server{outputs: []*output{out}, router: contentRouter{reports: map[string]ports.OutputPresented{}}}
	s.router = newContentRouter(s, s.release, s.bufferReferenced, func(name string) bool { return s.outputByNameExact(name) != nil })
	now := time.Unix(100, 0)
	s.router.settle(now, []ports.OutputPresented{{Output: "DP-2", Snapshot: first}})
	require.Empty(t, returned, "latest report remains retained")
	s.router.settle(now, []ports.OutputPresented{{Output: "DP-2", Snapshot: next}})
	require.Len(t, returned, 1)
	require.Same(t, first, <-returned)
	require.Same(t, next, s.router.reports["DP-2"].Snapshot)
	s.outputs = nil
	s.router.settle(now, nil)
	require.Len(t, returned, 1)
	require.Same(t, next, <-returned)
	require.Empty(t, s.router.reports)
}

// A flip carried over an evicted report must live in the successor's own
// lease: the evicted lease goes back to the output owner, which recycles it.
func TestPresentationRingOverflowCarriesFlipIntoOwnLease(t *testing.T) {
	p := ringOf()
	flip := &domain.FlipInfo{Seq: 7, Shows: map[domain.WindowID]uint64{3: 7}}
	// Two separate queues keep two distinct leases for the same output.
	var q1, q2 presented.Queue
	q1.Push(ports.OutputPresented{Output: "A", Flip: flip, Seen: map[domain.WindowID]uint64{1: 1}})
	q2.Push(ports.OutputPresented{Output: "A", Seen: map[domain.WindowID]uint64{1: 2}})
	old, next := q1.Unsent()[0], q2.Unsent()[0]
	require.NotSame(t, old.Snapshot, next.Snapshot)
	p.ring = append(p.ring, old)
	p.fill()
	require.False(t, p.makeRoomLocked(&next))
	// The old lease is back with its owner and gets recycled (overwritten).
	old.Snapshot.Flip = domain.FlipInfo{Seq: 99}
	clear(old.Snapshot.Shows)
	require.NotNil(t, next.Flip)
	require.Same(t, &next.Snapshot.Flip, next.Flip)
	require.EqualValues(t, 7, next.Flip.Seq)
	require.Equal(t, map[domain.WindowID]uint64{3: 7}, next.Flip.Shows)
}
