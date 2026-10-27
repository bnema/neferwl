package core

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// Every workspace lists its tiled columns and windows with settled
// rectangles; only the workspace on screen is visible.
func TestStateWorkspaceGeometry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := domain.Config{}
		cfg.Keyboard.CmdKey = "super"
		cfg.Layout.Overflow = "fixed"
		cfg.Layout.MaxColumns = 2
		cfg.Layout.Gaps = 10
		c, err := New(cfg, Channels{Commands: make(chan ports.ClientCommand, 16), Scenes: make(chan []domain.Scene, 1)}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		c.addScreen(ports.OutputInfo{Name: "OUT-1", Width: 1000, Height: 800})
		m := c.cur().mon
		shown := m.Current()
		shown.Columns = []Column{{Windows: []WindowID{1}}, {Windows: []WindowID{2, 3}}}
		m.normalize() // the trailing empty workspace, as in a session
		other := m.newWorkspace()
		other.Columns = []Column{{Windows: []WindowID{4}}}
		m.hidden = append(m.hidden, other)

		got := c.state().Workspaces
		if len(got) != 3 {
			t.Fatalf("%d workspaces: %+v", len(got), got)
		}
		ws := got[0]
		if ws.ID != shown.ID || ws.Number != 1 || ws.Output != "OUT-1" || !ws.Visible || ws.Overflow != "fixed" {
			t.Fatalf("%+v", ws)
		}
		want := []domain.ColumnState{
			{Rect: Rect{X: 10, Y: 10, W: 485, H: 780}, Windows: []domain.TileState{{ID: 1, Rect: Rect{X: 10, Y: 10, W: 485, H: 780}}}},
			{Rect: Rect{X: 505, Y: 10, W: 485, H: 780}, Windows: []domain.TileState{{ID: 2, Rect: Rect{X: 505, Y: 10, W: 485, H: 385}}, {ID: 3, Rect: Rect{X: 505, Y: 405, W: 485, H: 385}}}},
		}
		if len(ws.Columns) != len(want) {
			t.Fatalf("%+v", ws.Columns)
		}
		for i := range want {
			if ws.Columns[i].Rect != want[i].Rect || len(ws.Columns[i].Windows) != len(want[i].Windows) {
				t.Fatalf("column %d: %+v, want %+v", i, ws.Columns[i], want[i])
			}
			for j := range want[i].Windows {
				if ws.Columns[i].Windows[j] != want[i].Windows[j] {
					t.Errorf("column %d tile %d: %+v, want %+v", i, j, ws.Columns[i].Windows[j], want[i].Windows[j])
				}
			}
		}
		var found bool
		for _, w := range got[1:] {
			if w.ID == other.ID {
				found = true
				if w.Visible || w.Number != 0 || len(w.Columns) != 1 || w.Columns[0].Windows[0].ID != 4 {
					t.Errorf("hidden workspace: %+v", w)
				}
			} else if w.Visible {
				t.Errorf("workspace %d visible", w.ID)
			}
		}
		if !found {
			t.Error("second workspace missing")
		}

	})
}

// A change of geometry is a change of state; an unchanged one is not.
func TestStateWorkspaceChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := domain.Config{}
		cfg.Keyboard.CmdKey = "super"
		cfg.Layout.MaxColumns = 2
		c, err := New(cfg, Channels{Commands: make(chan ports.ClientCommand, 16), Scenes: make(chan []domain.Scene, 1)}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		c.addScreen(ports.OutputInfo{Name: "OUT-1", Width: 1000, Height: 800})
		w := c.cur().mon.Current()
		w.Columns = []Column{{Windows: []WindowID{1}}}
		a := c.state()
		if !sameState(a, c.state()) {
			t.Fatal("same layout reported as changed")
		}
		w.Columns = []Column{{Windows: []WindowID{1}}, {Windows: []WindowID{2}}}
		if sameState(a, c.state()) {
			t.Fatal("new column not seen")
		}

	})
}
