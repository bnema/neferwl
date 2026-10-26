package core

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

func TestRowRectsEqualWithoutShares(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := Rect{X: 0, Y: 0, W: 50, H: 103}
		c := Column{Windows: []WindowID{1, 2, 3}}
		if !slices.Equal(rowRects(r, c, 5), stackRects(r, 3, 5)) {
			t.Fatal(rowRects(r, c, 5))
		}
		c.Shares = []int{5000, 5000} // wrong length: ignored
		if !slices.Equal(rowRects(r, c, 5), stackRects(r, 3, 5)) {
			t.Fatal(rowRects(r, c, 5))
		}
		c.Shares = []int{5000, 3000, 3000} // wrong sum: ignored
		if !slices.Equal(rowRects(r, c, 5), stackRects(r, 3, 5)) {
			t.Fatal(rowRects(r, c, 5))
		}
		c.Shares = []int{5000, 3000, 2000}
		rows := rowRects(r, c, 5)
		if rows[0].H != 46 || rows[1].H != 27 || rows[2].H != 93-46-27 || rows[2].Y+rows[2].H != 103 {
			t.Fatal(rows)
		}

	})
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

func TestSharesResetAndTravel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := monitor()
		m.SetOutput(1000, 800)
		m.AddWindow(1)
		m.AddWindow(2)
		w := m.Current()
		w.ConsumeOrExpel(-1)
		if len(w.Columns) != 1 {
			t.Fatal(w.Columns)
		}
		w.Apply("set-window-height +20%")
		if !slices.Equal(w.Columns[0].Shares, []int{3000, 7000}) {
			t.Fatal(w.Columns[0].Shares)
		}
		// Moving the window inside the column swaps the shares with it.
		w.Apply(ActionMoveWindowUp)
		if !slices.Equal(w.Columns[0].Shares, []int{7000, 3000}) || w.Columns[0].Windows[0] != 2 {
			t.Fatal(w.Columns[0])
		}
		// The column takes its shares to another workspace.
		m.MoveToWorkspace(1, true)
		m.FocusNumber(2)
		if c := m.Current().Columns[0]; !slices.Equal(c.Shares, []int{7000, 3000}) {
			t.Fatal(c)
		}
		// MoveColumn keeps them.
		m.AddWindow(3)
		m.Current().FocusColumn(-1)
		m.Current().MoveColumn(1)
		if c := m.Current().Columns[1]; !slices.Equal(c.Shares, []int{7000, 3000}) {
			t.Fatal(m.Current().Columns)
		}
		// A window leaving resets the column to equal rows.
		m.RemoveWindow(2)
		m.AddWindow(4)
		w = m.Current()
		for _, c := range w.Columns {
			if c.Shares != nil {
				t.Fatal(w.Columns)
			}
		}
		// A window joining invalidates them: equal rows.
		w.Columns = []Column{{Windows: []WindowID{1, 3}, Shares: []int{8000, 2000}}}
		w.Columns[0].Windows = append(w.Columns[0].Windows, 4)
		h := heights(w)
		if h[1] != h[3] {
			t.Fatal(h)
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
