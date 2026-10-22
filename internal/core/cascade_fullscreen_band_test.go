package core

import "testing"

// Entering fullscreen gives a cascade column a band below its own; the
// others stay. Leaving keeps it there.
func TestCascadeFullscreenOwnBand(t *testing.T) {
	for _, tc := range []struct {
		name string
		do   func(c *Core, w *Workspace)
	}{
		{"toggle", func(_ *Core, w *Workspace) { w.ToggleFullscreen() }},
		{"client request", func(_ *Core, w *Workspace) { w.SetFullscreen(3, true) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, w := cascadeMovesCore(t, 3, 3, false)
			wantBands(t, w, []WindowID{1, 2, 3})
			tc.do(c, w)
			wantBands(t, w, []WindowID{1, 2}, []WindowID{3})
			wantFocus(t, c, "M", 3)
			if w.fullscreen != 3 {
				t.Fatalf("fullscreen %d, want 3", w.fullscreen)
			}
			w.ToggleFullscreen()
			wantBands(t, w, []WindowID{1, 2}, []WindowID{3})
		})
	}
}

func TestCascadeFullscreenMiddleAndAlone(t *testing.T) {
	c, w := cascadeMovesCore(t, 3, 3, false)
	w.FocusID(2)
	w.ToggleFullscreen()
	wantBands(t, w, []WindowID{1, 3}, []WindowID{2})
	wantFocus(t, c, "M", 2)

	_, w = cascadeMovesCore(t, 1, 3, false)
	w.ToggleFullscreen()
	wantBands(t, w, []WindowID{1})
}

func TestCascadeFullscreenExpelsFromStack(t *testing.T) {
	c, w := cascadeMovesCore(t, 2, 3, false)
	w.FocusID(2)
	c.applyAction(ActionConsumeOrExpelLeft) // 2 stacks under 1
	wantColumns(t, w, 1)
	w.ToggleFullscreen()
	wantBands(t, w, []WindowID{1}, []WindowID{2})
	if len(w.Columns[0].Windows) != 1 || w.fullscreen != 2 {
		t.Fatalf("columns %v fullscreen %d", w.Columns, w.fullscreen)
	}
	wantFocus(t, c, "M", 2)
}

func TestScrollFullscreenKeepsColumns(t *testing.T) {
	_, w := cascadeMovesCore(t, 3, 3, false)
	w.Overflow = OverflowScroll
	w.forgetBands()
	w.ToggleFullscreen()
	if len(w.Columns) != 3 {
		t.Fatalf("columns %d", len(w.Columns))
	}
}

func TestFullscreenColumnMovesToNeighborMonitor(t *testing.T) {
	for _, tc := range []struct {
		action Action
		screen string
	}{{ActionMoveColumnLeft, "L"}, {ActionMoveColumnRight, "R"}} {
		t.Run(tc.screen, func(t *testing.T) {
			c, w := cascadeMovesCore(t, 3, 3, true)
			w.ToggleFullscreen()
			c.applyAction(tc.action)
			wantColumns(t, w, 1, 2)
			dst := screenWorkspace(c, tc.screen)
			wantColumns(t, dst, 3)
			wantFocus(t, c, tc.screen, 3)
			if dst.fullscreen != 3 || w.fullscreen != 0 {
				t.Fatalf("fullscreen dst %d src %d", dst.fullscreen, w.fullscreen)
			}
		})
	}
}

func TestFullscreenColumnNoNeighborStays(t *testing.T) {
	c, w := cascadeMovesCore(t, 3, 3, false)
	w.ToggleFullscreen()
	c.applyAction(ActionMoveColumnLeft)
	c.applyAction(ActionMoveColumnRight)
	wantBands(t, w, []WindowID{1, 2}, []WindowID{3})
	if w.fullscreen != 3 {
		t.Fatalf("fullscreen %d", w.fullscreen)
	}
}
