package core

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
)

func TestFreeFloatMovesAcrossWorkspaces(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := monitor()
		m.SetOutput(1000, 800)
		m.AddWindow(1)
		m.AddWindow(2)
		m.Apply(ActionToggleFloating)
		w := m.Current()
		_, r, ok := w.FloatAt(2)
		if !ok {
			t.Fatal("float missing")
		}
		// Resize through the real drag path so the requested centre fits.
		w.DragFloat(2, r, domain.ResizeRight|domain.ResizeBottom, 100-r.W, 100-r.H, true)
		_, r, _ = w.FloatAt(2)
		w.DragFloat(2, r, 0, 200-(r.X+r.W/2), 640-(r.Y+r.H/2), false)
		m.MoveToWorkspace(1, false)
		to := m.Workspaces[1]
		if i := modelFloatIndex(to, 2); i < 0 || !to.Inspect().Floats[i].Free || to.Inspect().Floats[i].CX != 0.2 || to.Inspect().Floats[i].CY != 0.8 {
			t.Fatal(to.Floats)
		}
		// Stashed there, moved back, unstashed: still free at its place.
		m.FocusNumber(2)
		m.Apply(ActionToggleWindowStash)
		m.MoveToWorkspace(0, false)
		m.FocusNumber(1)
		m.Apply(ActionToggleWindowStash)
		w = m.Current()
		if i := modelFloatIndex(w, 2); i < 0 || !w.Inspect().Floats[i].Free || w.Inspect().Floats[i].CX != 0.2 {
			t.Fatal(w.Floats, w.Stash)
		}

	})
}

func TestFreeFloatReturnsAfterWorkspaceMove(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := monitor()
		m.SetOutput(1000, 800)
		m.AddWindow(1)
		m.AddWindow(2)
		m.Apply(ActionMaximizeColumn)
		m.Apply(ActionToggleFloating)
		m.MoveToWorkspace(1, false)
		m.FocusNumber(2)
		m.Current().FocusID(2)
		m.Apply(ActionToggleFloating)
		w := m.Current()
		if len(w.Columns) != 1 || w.Columns[0].Windows[0] != 2 || w.Columns[0].FullWidth {
			t.Fatal(w.Columns)
		}

	})
}
