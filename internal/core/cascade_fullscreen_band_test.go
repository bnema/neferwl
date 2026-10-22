package core

import "testing"

// Entering fullscreen gives a cascade column a band below its own; the
// others stay. Leaving keeps it there.
func TestCascadeFullscreenOwnBand(t *testing.T) {
	for _, tc := range []struct {
		name string
		do   func(w *Workspace)
	}{
		{"toggle", func(w *Workspace) { w.ToggleFullscreen() }},
		{"client request", func(w *Workspace) { w.SetFullscreen(3, true) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, w := cascadeMovesCore(t, 3, 3, false)
			wantBands(t, w, []WindowID{1, 2, 3})
			tc.do(w)
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

func TestFullscreenFloatMovesToNeighborMonitor(t *testing.T) {
	for _, tc := range []struct {
		action Action
		screen string
	}{{ActionMoveColumnLeft, "L"}, {ActionMoveColumnRight, "R"}} {
		t.Run(tc.screen, func(t *testing.T) {
			c, w := cascadeMovesCore(t, 2, 3, true)
			w.AddFloating(9, 5120, 2160)
			w.SetFullscreen(9, true)
			c.applyAction(tc.action)
			dst := screenWorkspace(c, tc.screen)
			if w.floatIndex(9) >= 0 || dst.floatIndex(9) < 0 || w.fullscreen != 0 || dst.fullscreen != 9 {
				t.Fatalf("float on src %v dst %v, fullscreen src %d dst %d", w.Floats, dst.Floats, w.fullscreen, dst.fullscreen)
			}
			wantFocus(t, c, tc.screen, 9)
			wantColumns(t, w, 1, 2)
		})
	}
}

func TestFullscreenFloatNoNeighborStays(t *testing.T) {
	c, w := cascadeMovesCore(t, 2, 3, false)
	w.AddFloating(9, 800, 600)
	w.SetFullscreen(9, true)
	c.applyAction(ActionMoveColumnLeft)
	if w.floatIndex(9) < 0 || w.fullscreen != 9 {
		t.Fatalf("float moved: %v fullscreen %d", w.Floats, w.fullscreen)
	}
}

func TestCascadeFullscreenRepeatedRequestKeepsBands(t *testing.T) {
	_, w := cascadeMovesCore(t, 3, 3, false)
	w.SetFullscreen(3, true)
	w.SetFullscreen(3, true)
	w.SetFullscreen(3, true)
	wantBands(t, w, []WindowID{1, 2}, []WindowID{3})
}

func TestCascadeFullscreenStackedFixups(t *testing.T) {
	for _, tc := range []struct {
		name     string
		focus    WindowID
		rest     WindowID
		slotKept bool
	}{{"last row", 3, 2, true}, {"first row", 2, 3, false}} {
		t.Run(tc.name, func(t *testing.T) {
			c, w := cascadeMovesCore(t, 3, 3, false)
			w.FocusID(3)
			c.applyAction(ActionConsumeOrExpelLeft) // 3 stacks under 2
			w.FocusID(tc.focus)
			w.Columns[1].Shares = []int{5000, 5000}
			w.Columns[1].Span = 3000
			w.Columns[1].Slot = 4
			w.SetFullscreen(tc.focus, true)
			wantBands(t, w, []WindowID{1, tc.rest}, []WindowID{tc.focus})
			col := w.Columns[1]
			if len(col.Windows) != 1 || col.Windows[0] != tc.rest || col.Shares != nil || col.Focus != 0 || (col.Slot == 4) != tc.slotKept {
				t.Fatalf("remaining column %+v", col)
			}
			if got, _ := w.Focused(); got != tc.focus {
				t.Fatalf("focus %d, want %d", got, tc.focus)
			}
			if w.Columns[2].Span != 0 || w.Columns[2].Slot != 0 {
				t.Fatalf("expelled column %+v", w.Columns[2])
			}
		})
	}
}

func TestCascadeFullscreenKeepsSlotOfIntactColumn(t *testing.T) {
	_, w := cascadeMovesCore(t, 3, 3, false)
	w.Columns[2].Slot = 4
	w.ToggleFullscreen()
	if i := w.columnOf(3); w.Columns[i].Slot != 4 {
		t.Fatalf("slot %d, want 4", w.Columns[i].Slot)
	}
}

func TestCascadeFullscreenAfterMaximizeRestores(t *testing.T) {
	_, w := cascadeMovesCore(t, 3, 3, false)
	w.FocusID(1)
	w.ToggleFullWidth()
	before := columnOrder(w)
	w.FocusID(3)
	w.ToggleFullscreen()
	w.ToggleFullscreen()
	w.FocusID(1)
	w.ToggleFullWidth()
	t.Logf("before %v bands after %v", before, columnOrder(w))
	wantBands(t, w, []WindowID{1, 2, 3})
}

func TestFullscreenMoveEdges(t *testing.T) {
	t.Run("destination fullscreen elsewhere", func(t *testing.T) {
		c, w := cascadeMovesCore(t, 2, 3, true)
		dst := screenWorkspace(c, "R")
		dst.AddWindow(20)
		dst.SetFullscreen(20, true)
		w.ToggleFullscreen()
		c.applyAction(ActionMoveColumnRight)
		if dst.fullscreen != 2 {
			t.Fatalf("fullscreen %d, want 2", dst.fullscreen)
		}
		wantFocus(t, c, "R", 2)
	})
	t.Run("scroll layout", func(t *testing.T) {
		c, w := cascadeMovesCore(t, 2, 3, true)
		w.Overflow = OverflowScroll
		w.ToggleFullscreen()
		c.applyAction(ActionMoveColumnLeft)
		dst := screenWorkspace(c, "L")
		wantColumns(t, dst, 2)
		if dst.fullscreen != 2 || w.fullscreen != 0 {
			t.Fatalf("fullscreen dst %d src %d", dst.fullscreen, w.fullscreen)
		}
	})
	t.Run("source floats promoted", func(t *testing.T) {
		for _, float := range []bool{false, true} {
			c, w := cascadeMovesCore(t, 2, 3, true)
			w.AddFloating(9, 100, 100)
			w.SetFullscreen(9, true)
			if !float {
				w.FocusID(2)
				w.ToggleFullscreen()
			}
			demoted := w.Floats[0].below
			c.applyAction(ActionMoveColumnRight)
			if float {
				demoted = false
			}
			_ = demoted
			for _, f := range w.Floats {
				if f.below {
					t.Fatalf("float=%v: float %d still demoted", float, f.ID)
				}
			}
		}
	})
}
