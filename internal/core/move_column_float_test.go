package core

import (
	"testing"
	"testing/synctest"
)

// MoveColumn does nothing while a floating or stashed window has the focus:
// the window stays on its workspace, the focus stays where it was and the
// neighbor workspace is left as it was.
func TestMoveColumnIsNoOpWhileFloatFocused(t *testing.T) {
	for _, kind := range []struct {
		name  string
		setup func(t *testing.T, w *Workspace)
		// held reports where the focused window 2 stays.
		held func(w *Workspace) bool
	}{
		{"float", func(t *testing.T, w *Workspace) {
			w.AddWindow(1)
			w.AddFloating(2, 100, 100)
		}, func(w *Workspace) bool { return modelFloatIndex(w, 2) >= 0 }},
		{"stash", func(t *testing.T, w *Workspace) {
			w.AddWindow(1)
			w.AddWindow(2)
			w.ToggleWindowStash()
			if !w.StashShown() {
				t.Fatal("setup: stash not shown")
			}
		}, func(w *Workspace) bool { return w.StashIndex(2) >= 0 }},
	} {
		for _, tc := range []struct {
			action Action
			from   string
			to     string
			screen int
		}{
			{ActionMoveColumnRight, "L", "R", 0},
			{ActionMoveColumnLeft, "R", "L", 1},
		} {
			t.Run(kind.name+"/"+string(tc.action), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					c := layoutCore(tc.screen, tile{"L", 0, 0, 900, 600}, tile{"R", 900, 0, 900, 600})
					w := c.cur().mon.Current()
					kind.setup(t, w)
					if !w.OnFloat() {
						t.Fatal("setup: float or stash not focused")
					}
					if id, _ := w.Focused(); id != 2 {
						t.Fatalf("setup: focused window %d, want 2", id)
					}
					dst := screenWorkspace(c, tc.to)
					dst.AddWindow(3)
					dstFocus, _ := dst.Focused()
					if dstFocus != 3 {
						t.Fatalf("setup: destination focus %d, want 3", dstFocus)
					}

					c.applyAction(tc.action)

					if !kind.held(w) || modelFloatIndex(dst, 2) >= 0 || dst.StashIndex(2) >= 0 {
						t.Fatalf("window 2 left src: src floats %v stash %v, dst floats %v stash %v", w.Floats, w.Stash, dst.Floats, dst.Stash)
					}
					wantColumns(t, w, 1)
					wantColumns(t, dst, 3)
					if got, _ := dst.Focused(); got != dstFocus {
						t.Fatalf("destination focus %d, want %d", got, dstFocus)
					}
					wantFocus(t, c, tc.from, 2)
				})
			})
		}
	}
}
