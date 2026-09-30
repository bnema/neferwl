package sessionsecurity

import (
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestGateTransitionOwnership(t *testing.T) {
	var g Gate
	if got := g.Snapshot(); got != (ports.SecurityState{}) {
		t.Fatalf("zero state %v", got)
	}
	first, err := g.Engage()
	if err != nil || !first.Protected || first.Generation != 1 {
		t.Fatalf("engage %v %v", first, err)
	}
	second, err := g.Engage()
	if err != nil || !second.Protected || second.Generation != 2 {
		t.Fatalf("takeover %v %v", second, err)
	}
	if _, err := g.Release(first.Generation); !errors.Is(err, ErrStaleRelease) {
		t.Fatalf("old owner released: %v", err)
	}
	if got := g.Snapshot(); got != second {
		t.Fatalf("stale release changed gate: %v", got)
	}
	released, err := g.Release(second.Generation)
	if err != nil || released.Protected || released.Generation != 3 {
		t.Fatalf("release %v %v", released, err)
	}
	if _, err := g.Release(second.Generation); !errors.Is(err, ErrStaleRelease) {
		t.Fatalf("duplicate release: %v", err)
	}
}

func TestGateExhaustionStaysProtected(t *testing.T) {
	var g Gate
	g.state.Store(math.MaxUint64)
	before := g.Snapshot()
	if _, err := g.Engage(); !errors.Is(err, ErrGenerationExhausted) {
		t.Fatalf("engage: %v", err)
	}
	if _, err := g.Release(before.Generation); !errors.Is(err, ErrGenerationExhausted) {
		t.Fatalf("release: %v", err)
	}
	if g.Snapshot() != before {
		t.Fatal("exhaustion changed protection")
	}
}

func TestGatePenultimateReleaseStaysProtected(t *testing.T) {
	var g Gate
	g.state.Store(math.MaxUint64 - 2)
	before := g.Snapshot()
	if _, err := g.Release(before.Generation); !errors.Is(err, ErrGenerationExhausted) {
		t.Fatalf("penultimate release: %v", err)
	}
	if g.Snapshot() != before || !g.Snapshot().Protected {
		t.Fatal("release exhausted generation while unprotected")
	}
	next, err := g.Engage()
	if err != nil || !next.Protected || next.Generation != before.Generation+1 {
		t.Fatalf("final engagement: %v %v", next, err)
	}
	if _, err := g.Release(next.Generation); !errors.Is(err, ErrGenerationExhausted) {
		t.Fatalf("final release: %v", err)
	}
}

func TestGateSnapshotCoherence(t *testing.T) {
	var g Gate
	var readers sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				s := g.Snapshot()
				// This test alternates engage/release without takeovers:
				// odd generations protected, even generations unlocked.
				if s.Protected != (s.Generation&1 != 0) {
					t.Errorf("torn state %v", s)
					return
				}
			}
		})
	}
	for range 1000 {
		s, err := g.Engage()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.Release(s.Generation); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	readers.Wait()
}
