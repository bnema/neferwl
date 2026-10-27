package workspace

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core/layout"
)

func TestColumnBorders(t *testing.T) {
	for _, tc := range []struct {
		name     string
		overflow Overflow
		n        int
		want     bool
	}{
		{"scroll", OverflowScroll, 5, true},
		{"cascade", OverflowCascade, 5, true},
		{"fixed row", OverflowFixed, 3, true},
		{"fixed spiral", OverflowFixed, 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := Workspace{Lanes: layout.Lanes{Overflow: tc.overflow, MaxColumns: 3, Columns: make([]Column, tc.n)}}
				if got := columnBorders(w.Engine(), w.Snapshot()); got != tc.want {
					t.Fatalf("column borders = %v, want %v", got, tc.want)
				}

			})
		})
	}
}

// A layout that does not claim overridesWidth must size columns like the base:
// generic loops then skip its columnWidth. This catches a forgotten flag.
func TestOverridesWidthMatchesColumnWidth(t *testing.T) {
	forEachConformance(t, func(t *testing.T, l layout.Layout, w *Workspace, _, n int, _ string) {
		if l.OverridesWidth() || n == 0 {
			return
		}
		v := w.Snapshot()
		for _, ignore := range []bool{false, true} {
			for i := range v.Columns {
				if got, want := l.ColumnWidth(v, i, ignore), (layout.Base{}).ColumnWidth(v, i, ignore); got != want {
					t.Fatalf("%s column %d (ignore %v): width %d, base %d: set overridesWidth", l.Name(), i, ignore, got, want)
				}
			}
		}
	})
}
