package workspace

import (
	"slices"
	"testing"
	"testing/synctest"
)

func TestMRUOrder(t *testing.T) {
	tests := []struct {
		name string
		run  func(w *Workspace)
		want []WindowID
	}{
		{"follows focus", func(w *Workspace) {
			// Columns 0, 2, 1 in turn: the last one used goes first.
			focusNote(w, 1)
			focusNote(w, 3)
			focusNote(w, 2)
		}, []WindowID{2, 3, 1}},
		{"back to the oldest", func(w *Workspace) {
			focusNote(w, 1)
			focusNote(w, 3)
			focusNote(w, 2)
			focusNote(w, 1)
		}, []WindowID{1, 2, 3}},
		{"never focused follow by position", func(w *Workspace) {
			focusNote(w, 3)
		}, []WindowID{3, 1, 2}},
		{"closed windows are pruned", func(w *Workspace) {
			focusNote(w, 1)
			focusNote(w, 3)
			focusNote(w, 2)
			w.RemoveWindow(3)
		}, []WindowID{2, 1}},
		{"a moved column keeps its rank", func(w *Workspace) {
			focusNote(w, 1)
			focusNote(w, 3)
			focusNote(w, 2)
			w.MoveColumn(1)
			w.noteFocus()
		}, []WindowID{2, 3, 1}},
		{"a column taken to another workspace leaves recent", func(w *Workspace) {
			focusNote(w, 1)
			focusNote(w, 3)
			focusNote(w, 2)
			if _, ok := w.takeColumn(); !ok {
				t.Fatal("no column taken")
			}
			if slices.Contains(w.recent, 2) {
				t.Fatalf("recent %v keeps the taken window", w.recent)
			}
			w.noteFocus()
		}, []WindowID{3, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := threeColumns()
				tt.run(w)
				if got := w.SwitchOrder(); !slices.Equal(got, tt.want) {
					t.Fatalf("order %v, want %v", got, tt.want)
				}
				// recent never keeps a closed window nor two of one column.
				seen := map[int]bool{}
				for _, id := range w.recent {
					i := w.ColumnOf(id)
					if i < 0 || seen[i] {
						t.Fatalf("recent %v keeps window %d (column %d)", w.recent, id, i)
					}
					seen[i] = true
				}

			})
		})
	}
}

func TestMRUSwitchOrderNeedsTwoColumns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := workspace()
		if w.SwitchOrder() != nil {
			t.Fatal("order of an empty workspace")
		}
		w.AddWindow(1)
		w.noteFocus()
		if w.SwitchOrder() != nil {
			t.Fatal("order of one column")
		}

	})
}

func TestMRUCapped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := workspace()
		w.SetMaxColumns(3)
		for id := WindowID(1); id <= switcherRecentMax+10; id++ {
			w.AddWindow(id)
			w.noteFocus()
		}
		if len(w.recent) != switcherRecentMax {
			t.Fatalf("recent holds %d", len(w.recent))
		}

	})
}

func focusNote(w *Workspace, id WindowID) {
	w.FocusID(id)
	w.noteFocus()
}
