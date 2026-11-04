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

// Held windows are output-sized and aligned like the cover but never cover.
func TestHeldGeometry(t *testing.T) {
	out := Rect{W: 1000, H: 600}
	usable := Rect{Y: 30, W: 1000, H: 570}
	for _, tc := range []struct {
		overflow   Overflow
		kind       InsertKind
		wantTarget func(w *Lanes, v View) int
	}{
		{OverflowScroll, InsertDefault, func(w *Lanes, v View) int { vf := v.WithFacts(); return vf.ColumnX(1) - out.X }},
		{OverflowCascade, InsertNewLane, func(w *Lanes, v View) int { return w.Band(1) * usable.H }},
	} {
		t.Run(string(tc.overflow), func(t *testing.T) {
			w := &Lanes{MaxColumns: 2, Overflow: tc.overflow, Gaps: 8, Usable: usable, Output: out}
			w.Columns = append(w.Columns, Column{Windows: []WindowID{1}})
			w.ColumnInserted(0, InsertDefault)
			w.Columns = append(w.Columns, Column{Windows: []WindowID{2}})
			w.ColumnInserted(1, tc.kind)
			l := w.Engine()

			base := w.Snapshot()
			if got := l.ColumnWidth(base, 1, false); got == out.W {
				t.Fatalf("plain column already output wide: %d", got)
			}
			w.Held = []WindowID{2}
			w.Focus = 1
			v := w.Snapshot()
			if got := l.ColumnWidth(v, 1, false); got != out.W {
				t.Errorf("held width %d, want %d", got, out.W)
			}
			if got, want := l.ScrollTarget(v), tc.wantTarget(w, v); got != want {
				t.Errorf("held scroll target %d, want %d", got, want)
			}
			v.View = l.ScrollTarget(v)
			if l.Covering(v) {
				t.Error("held-only view reports Covering")
			}
			// The active cover still covers once aligned.
			w.Held, w.Fullscreen = nil, 2
			v = w.Snapshot()
			v.View = l.ScrollTarget(v)
			if !l.Covering(v) {
				t.Error("aligned cover does not report Covering")
			}
		})
	}
}
