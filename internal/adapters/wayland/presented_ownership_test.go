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
	s := &Server{outputs: []*output{out}, reports: make(map[string]ports.OutputPresented)}
	now := time.Unix(100, 0)
	s.releaseHeld(now, []ports.OutputPresented{{Output: "DP-2", Snapshot: first}})
	require.Empty(t, returned, "latest report remains retained")
	s.releaseHeld(now, []ports.OutputPresented{{Output: "DP-2", Snapshot: next}})
	require.Len(t, returned, 1)
	require.Same(t, first, <-returned)
	require.Same(t, next, s.reports["DP-2"].Snapshot)
	s.outputs = nil
	s.releaseHeld(now, nil)
	require.Len(t, returned, 1)
	require.Same(t, next, <-returned)
	require.Empty(t, s.reports)
}
