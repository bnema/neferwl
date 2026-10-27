package core

import "testing"

// MoveColumn does nothing while a floating window has the focus: the float
// stays on its workspace and the focus stays where it was.
func TestMoveColumnIsNoOpWhileFloatFocused(t *testing.T) {
	for _, tc := range []struct {
		action Action
		from   string
		to     string
		screen int
	}{
		{ActionMoveColumnRight, "L", "R", 0},
		{ActionMoveColumnLeft, "R", "L", 1},
	} {
		t.Run(string(tc.action), func(t *testing.T) {
			c := layoutCore(tc.screen, tile{"L", 0, 0, 900, 600}, tile{"R", 900, 0, 900, 600})
			w := c.cur().mon.Current()
			w.AddWindow(1)
			w.AddFloating(2, 100, 100)
			if !w.OnFloat() {
				t.Fatal("setup: float not focused")
			}
			if id, _ := w.Focused(); id != 2 {
				t.Fatalf("setup: focused window %d, want 2", id)
			}
			dst := screenWorkspace(c, tc.to)

			c.applyAction(tc.action)

			if modelFloatIndex(w, 2) < 0 || modelFloatIndex(dst, 2) >= 0 {
				t.Fatalf("float on src %v dst %v", w.Floats, dst.Floats)
			}
			wantColumns(t, w, 1)
			wantFocus(t, c, tc.from, 2)
		})
	}
}
