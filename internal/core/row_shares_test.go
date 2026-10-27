package core

import (
	"slices"
	"testing"
	"testing/synctest"
)

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
