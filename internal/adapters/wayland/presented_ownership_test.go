package wayland

import (
	"testing"
	"time"

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
