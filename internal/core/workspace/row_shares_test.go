package workspace

import (
	"slices"
	"testing"
	"testing/synctest"
)

func heights(w *Workspace) map[WindowID]int {
	out := map[WindowID]int{}
	for _, p := range w.Layout() {
		if !p.Floating {
			out[p.ID] = p.Rect.H
		}
	}
	return out
}

func sum(v []int) int {
	t := 0
	for _, x := range v {
		t += x
	}
	return t
}

func TestResizeRow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, o := range []Overflow{OverflowScroll, OverflowFixed} {
			w := workspace()
			w.Overflow = o
			w.SetOutput(1000, 800)
			w.Columns = []Column{{Windows: []WindowID{1, 2}}}
			w.Apply("set-window-height +10%")
			if !slices.Equal(w.Columns[0].Shares, []int{6000, 4000}) {
				t.Fatal(o, w.Columns[0].Shares)
			}
			h := heights(w)
			if h[1] <= h[2] {
				t.Fatal(o, h)
			}
			for range 10 {
				w.Apply("set-window-height +10%")
			}
			if !slices.Equal(w.Columns[0].Shares, []int{9000, 1000}) {
				t.Fatal(o, w.Columns[0].Shares)
			}
			for range 10 {
				w.Apply("set-window-height -10%")
			}
			if !slices.Equal(w.Columns[0].Shares, []int{1000, 9000}) {
				t.Fatal(o, w.Columns[0].Shares)
			}
		}

	})
}

func TestResizeRowThree(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := workspace()
		w.SetOutput(1000, 800)
		w.Columns = []Column{{Windows: []WindowID{1, 2, 3}, Focus: 1}}
		w.Apply("set-window-height +10%")
		s := w.Columns[0].Shares
		if sum(s) != 10000 || s[1] != 4333 || s[0] < 1000 || s[2] < 1000 {
			t.Fatal(s)
		}
		for _, step := range []string{"+25%", "+25%", "-7%", "-100%", "+100%", "-3%"} {
			w.Apply(Action("set-window-height " + step))
			s := w.Columns[0].Shares
			if sum(s) != 10000 || slices.Min(s) < 1000 {
				t.Fatal(step, s)
			}
		}
		if s := w.Columns[0].Shares; s[1] != 7700 {
			t.Fatal(s)
		}
		// Proportional: the others shrink by their room above the minimum.
		w.Columns[0].Shares = []int{5000, 2000, 3000}
		w.Columns[0].Focus = 1
		w.Apply("set-window-height +20%")
		if s := w.Columns[0].Shares; !slices.Equal(s, []int{3667, 4000, 2333}) {
			t.Fatal(s)
		}

	})
}

func TestResizeRowNoops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := workspace()
		w.Columns = []Column{{Windows: []WindowID{1}}}
		w.Apply("set-window-height +10%")
		if w.Columns[0].Shares != nil {
			t.Fatal(w.Columns[0].Shares)
		}
		w.Columns = []Column{{Windows: []WindowID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}}}
		w.Apply("set-window-height +10%")
		if w.Columns[0].Shares != nil {
			t.Fatal(w.Columns[0].Shares)
		}
		w.Columns = []Column{{Windows: []WindowID{1, 2}}}
		w.AddFloating(3, 10, 10)
		w.Apply("set-window-height +10%")
		if w.Columns[0].Shares != nil {
			t.Fatal(w.Columns[0].Shares)
		}
		// A fullscreen window hides its column.
		w = workspace()
		w.Columns = []Column{{Windows: []WindowID{1, 2}}}
		w.SetFullscreen(1, true)
		w.Apply("set-window-height +10%")
		if w.Columns[0].Shares != nil {
			t.Fatal(w.Columns[0].Shares)
		}

	})
}

func TestOverviewPreviewUsesShares(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := workspace()
		w.SetOutput(1000, 800)
		w.Columns = []Column{{Windows: []WindowID{1, 2}, Shares: []int{7000, 3000}}}
		tiles, _, _ := w.previewTiles()
		h := map[WindowID]int{}
		for _, p := range tiles {
			h[p.ID] = p.Rect.H
		}
		live := heights(w)
		if h[1] != live[1] || h[2] != live[2] {
			t.Fatal(h, live)
		}

	})
}
