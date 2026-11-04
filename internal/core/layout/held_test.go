package layout

import (
	"slices"
	"testing"
)

// Snapshot carries held fullscreen windows without copying them.
func TestSnapshotCarriesHeld(t *testing.T) {
	w := &Lanes{Overflow: OverflowScroll, Fullscreen: 1, Held: []WindowID{2, 3}}
	v := w.Snapshot()
	if !slices.Equal(v.Held, w.Held) {
		t.Fatalf("Held = %v, want %v", v.Held, w.Held)
	}
	if &v.Held[0] != &w.Held[0] {
		t.Fatal("Snapshot copied Held instead of sharing its backing slice")
	}
	if n := testing.AllocsPerRun(100, func() { v = w.Snapshot() }); n != 0 {
		t.Fatalf("Snapshot allocated %v times", n)
	}
	if v := (&Lanes{Overflow: OverflowScroll}).Snapshot(); v.Held != nil {
		t.Fatalf("empty Held = %v, want nil", v.Held)
	}
}
