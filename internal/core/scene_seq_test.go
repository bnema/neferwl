package core_test

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// A publish that changes nothing on an output keeps that output's Seq, so
// its owner does not recompose; a real change takes a new one.
func TestSceneSeqKeptWhenNothingChanged(t *testing.T) {
	r := startSwipe(t, nil)
	first := r.mapWindow(t, 1)[0]
	if first.Seq == 0 {
		t.Fatal("scene without Seq")
	}
	// Focus-column-left at the left edge changes nothing but still publishes.
	again := r.key(t, "Left", ports.ModAlt)[0]
	if again.Seq != first.Seq || !again.SameAs(first) {
		t.Fatalf("Seq %d, want the held %d", again.Seq, first.Seq)
	}
	next := r.mapWindow(t, 2)[0]
	if next.Seq <= first.Seq || len(next.Windows) != 2 {
		t.Fatalf("Seq %d after mapping a window (was %d), %d windows", next.Seq, first.Seq, len(next.Windows))
	}
}
