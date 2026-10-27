package layout

import (
	"slices"
	"testing"
	"testing/synctest"
)

// The insert kind tells the cascade state what a new column means for bands:
// its own band, the band position of the column it displaces, or nothing.
func TestCascadeInsertKinds(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind InsertKind
		want []bool // band starts after inserting at 1, before: [true, false, true]
	}{
		{"default", InsertDefault, []bool{true, false, false, true}},
		{"new lane", InsertNewLane, []bool{true, true, false, true}},
		{"join displaced", InsertJoinDisplaced, []bool{true, false, false, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := Lanes{Overflow: OverflowCascade, MaxColumns: 3}
				for i := WindowID(1); i <= 3; i++ {
					w.Columns = append(w.Columns, Column{Windows: []WindowID{i}})
				}
				w.SettleLayout()
				w.SetBandStart(0, true)
				w.SetBandStart(1, false)
				w.SetBandStart(2, true)
				w.Columns = slices.Insert(w.Columns, 1, Column{Windows: []WindowID{9}})
				w.ColumnInserted(1, tc.kind)
				v := w.Snapshot()
				for i, want := range tc.want {
					if got := v.bandStart(i); got != want {
						t.Fatalf("band start %d = %v, want %v (all: %v)", i, got, want, w.cascadeState().bandStart)
					}
				}

			})
		})
	}
	// Inserting before a band start hands the start to the new column.
	w := Lanes{Overflow: OverflowCascade, MaxColumns: 3}
	for i := WindowID(1); i <= 3; i++ {
		w.Columns = append(w.Columns, Column{Windows: []WindowID{i}})
	}
	w.SettleLayout()
	w.SetBandStart(2, true)
	w.Columns = slices.Insert(w.Columns, 2, Column{Windows: []WindowID{9}})
	w.ColumnInserted(2, InsertJoinDisplaced)
	if v := w.Snapshot(); !v.bandStart(2) || v.bandStart(3) {
		t.Fatalf("band starts %v: the new column must take the displaced start", w.cascadeState().bandStart)
	}
}
