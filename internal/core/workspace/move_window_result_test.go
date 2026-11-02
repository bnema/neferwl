package workspace

import "testing"

func TestMoveWindowResult(t *testing.T) {
	for _, tc := range []struct {
		name              string
		overflow          Overflow
		windows, row, dir int
		want              bool
	}{
		{"scroll row down", OverflowScroll, 2, 0, 1, true},
		{"scroll row up", OverflowScroll, 2, 1, -1, true},
		{"scroll top", OverflowScroll, 2, 0, -1, false},
		{"scroll bottom", OverflowScroll, 2, 1, 1, false},
		{"scroll single up", OverflowScroll, 1, 0, -1, false},
		{"scroll single down", OverflowScroll, 1, 0, 1, false},
		{"cascade band move", OverflowCascade, 1, 0, 1, true},
		{"cascade first band edge", OverflowCascade, 1, 0, -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := workspace()
			w.SetOverflow(tc.overflow)
			w.SetMaxColumns(1)
			w.Columns = []Column{{Windows: []WindowID{1}, Focus: tc.row}}
			if tc.windows == 2 {
				w.Columns[0].Windows = append(w.Columns[0].Windows, 2)
			}
			if tc.overflow == OverflowCascade {
				w.AddWindow(3)
				w.FocusID(1)
			}
			if got := w.MoveWindow(tc.dir); got != tc.want {
				t.Fatalf("MoveWindow = %t, want %t", got, tc.want)
			}
		})
	}
}
