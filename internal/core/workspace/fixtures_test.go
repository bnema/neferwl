package workspace

import (
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/core/layout"
)

// columnOrder is the first window of each column, in column order.
func columnOrder(w *Workspace) []WindowID {
	var ids []WindowID
	for _, c := range w.Columns {
		ids = append(ids, c.Windows[0])
	}
	return ids
}

// wantBands checks the cascade bands, each by the first window of its
// columns.
func wantBands(t *testing.T, w *Workspace, want ...[]WindowID) {
	t.Helper()
	var got [][]WindowID
	w.WalkBands(func(_, first, last int) bool {
		got = append(got, columnOrder(&Workspace{Lanes: layout.Lanes{Columns: w.Columns[first : last+1]}}))
		return true
	})
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("bands %v, want %v", got, want)
	}
}
