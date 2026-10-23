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
			w.AddDialog(9, 2, 5120, 2160)
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
	w.AddDialog(9, 2, 800, 600)
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
	w.FocusID(3)
	w.ToggleFullscreen()
	w.ToggleFullscreen()
	w.FocusID(1)
	w.ToggleFullWidth()
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
		c, w := cascadeMovesCore(t, 2, 3, true)
		w.AddFloating(9, 100, 100)
		w.FocusID(2)
		w.ToggleFullscreen()
		// A fullscreen keeps a float demoted without it covering the output.
		w.Floats[0].below = true
		if len(w.Floats) != 1 || !w.Floats[0].below || w.fullscreen != 2 {
			t.Fatalf("setup: float not demoted: %+v", w.Floats)
		}
		c.applyAction(ActionMoveColumnRight)
		if len(w.Floats) != 1 || w.Floats[0].below {
			t.Fatalf("float still demoted: %+v", w.Floats)
		}
	})
}

func TestCascadeFullscreenRestoresAcrossBand(t *testing.T) {
	_, w := cascadeMovesCore(t, 3, 3, false)
	w.FocusID(1)
	w.ToggleFullWidth() // X=1 alone; 2 and 3 pushed below
	wantBands(t, w, []WindowID{1}, []WindowID{2, 3})
	w.FocusID(2)
	w.ToggleFullscreen()
	wantBands(t, w, []WindowID{1}, []WindowID{3}, []WindowID{2})
	w.ToggleFullscreen()
	w.FocusID(1)
	w.ToggleFullWidth()
	wantBands(t, w, []WindowID{1, 2, 3})
}

func TestFullscreenMoveDestinations(t *testing.T) {
	t.Run("arrival focused despite left mark", func(t *testing.T) {
		// The destination has a pending "left" mark on the window, which
		// mayCover would refuse unless it is focused; the arrival is
		// focused, so it ends fullscreen.
		c, w := cascadeMovesCore(t, 2, 3, true)
		dst := screenWorkspace(c, "R")
		dst.left = 2
		w.ToggleFullscreen()
		c.applyAction(ActionMoveColumnRight)
		wantFocus(t, c, "R", 2)
		if dst.fullscreen != 2 {
			t.Fatalf("fullscreen %d, want 2", dst.fullscreen)
		}
	})
	t.Run("fullscreen window expelled from a stack then moved", func(t *testing.T) {
		c, w := cascadeMovesCore(t, 3, 3, true)
		w.FocusID(3)
		c.applyAction(ActionConsumeOrExpelLeft) // 3 under 2
		w.ToggleFullscreen()                    // 3 expelled to its own band
		c.applyAction(ActionMoveColumnLeft)
		wantColumns(t, w, 1, 2)
		dst := screenWorkspace(c, "L")
		wantColumns(t, dst, 3)
		if dst.fullscreen != 3 || len(w.Columns[1].Windows) != 1 {
			t.Fatalf("fullscreen %d, source column %v", dst.fullscreen, w.Columns[1].Windows)
		}
	})
	t.Run("destination with columns gets its own band", func(t *testing.T) {
		c, w := cascadeMovesCore(t, 1, 3, true)
		dst := screenWorkspace(c, "R")
		dst.Overflow = OverflowCascade
		dst.SetMaxColumns(3)
		dst.AddWindow(20)
		dst.AddWindow(21)
		w.ToggleFullscreen()
		c.applyAction(ActionMoveColumnRight)
		wantBands(t, dst, []WindowID{20, 21}, []WindowID{1})
		if dst.fullscreen != 1 {
			t.Fatalf("fullscreen %d", dst.fullscreen)
		}
	})
}

// A band the user shaped is not looked past: P, pushed down from F, then
// moved past Y on purpose, stays there.
func TestRestoreBandDoesNotLookPastUserBand(t *testing.T) {
	c, w := cascadeMovesCore(t, 3, 3, false)
	w.FocusID(1)
	w.ToggleFullWidth() // {1} {2 3}
	w.FocusID(2)
	c.applyAction(ActionMoveWindowDown) // {1} {3} {2}
	wantBands(t, w, []WindowID{1}, []WindowID{3}, []WindowID{2})
	w.FocusID(1)
	w.ToggleFullWidth()
	wantBands(t, w, []WindowID{1, 3}, []WindowID{2})
}

func TestCascadeFullscreenHorizontalKeepsCover(t *testing.T) {
	for _, floating := range []bool{false, true} {
		t.Run(map[bool]string{false: "tile", true: "dialog"}[floating], func(t *testing.T) {
			c, w := cascadeMovesCore(t, 2, 3, true)
			id := WindowID(2)
			if floating {
				id = 9
				w.AddDialog(id, 2, 800, 600)
			}
			w.SetFullscreen(id, true)
			c.applyAction(ActionFocusColumnLeft)
			if c.cur().name() != "L" || w.fullscreen != id {
				t.Fatalf("screen %+v fullscreen %d", c.cur(), w.fullscreen)
			}
		})
	}
}

func TestCascadeFullscreenUpReachesPreviousBand(t *testing.T) {
	c, w := cascadeMovesCore(t, 2, 3, false)
	w.ToggleFullscreen()
	c.applyAction(ActionFocusWindowUp)
	if w.fullscreen != 0 {
		t.Fatalf("fullscreen %d", w.fullscreen)
	}
	wantFocus(t, c, "M", 1)
}

func TestCascadeFullscreenFloatEdgePrefersWorkspace(t *testing.T) {
	c, _ := cascadeMovesCore(t, 1, 3, true)
	m := c.cur().mon
	m.Focus(1)
	m.AddWindow(2)
	w := m.Current()
	w.Overflow = OverflowCascade
	w.AddDialog(9, 2, 800, 600)
	w.SetFullscreen(9, true)
	c.applyAction(ActionFocusColumnLeft)
	wantFocus(t, c, "M", 1)
	if w.fullscreen != 9 {
		t.Fatalf("fullscreen %d", w.fullscreen)
	}
}

func TestCascadeFullscreenFloatChildHorizontalReturnsToCover(t *testing.T) {
	for _, sides := range []bool{false, true} {
		for _, a := range []Action{ActionFocusColumnLeft, ActionFocusColumnRight} {
			t.Run(string(a)+map[bool]string{false: "/no-edge", true: "/edge"}[sides], func(t *testing.T) {
				c, w := cascadeMovesCore(t, 2, 3, sides)
				w.AddDialog(9, 2, 800, 600)
				w.SetFullscreen(9, true)
				w.AddDialog(10, 9, 100, 100)
				c.applyAction(a)
				wantFocus(t, c, "M", 9)
				if w.fullscreen != 9 {
					t.Fatalf("fullscreen %d", w.fullscreen)
				}
			})
		}
	}
}

func TestRestoreBandAcrossPromotedFloatBand(t *testing.T) {
	for _, tc := range []struct {
		name     string
		maximize bool // the user maximizes the promoted column before 1 comes back
		want     [][]WindowID
	}{
		{"an automatic band is crossed", false, [][]WindowID{{1, 2, 3}, {9}}},
		{"a maximized band stops the restore", true, [][]WindowID{{1}, {9}, {2, 3}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, w := cascadeMovesCore(t, 3, 3, false)
			w.FocusID(1)
			w.ToggleFullWidth()
			w.AddFloating(9, 800, 600)
			w.SetFullscreen(9, true)
			wantBands(t, w, []WindowID{1}, []WindowID{9}, []WindowID{2, 3})
			w.SetFullscreen(9, false)
			if tc.maximize {
				w.FocusID(9)
				w.ToggleFullWidth()
			}
			w.FocusID(1)
			w.ToggleFullWidth()
			wantBands(t, w, tc.want...)
		})
	}
}

func TestPromotedFullscreenFloatMovesToNeighborMonitor(t *testing.T) {
	for _, tc := range []struct {
		a      Action
		screen string
	}{{ActionMoveColumnLeft, "L"}, {ActionMoveColumnRight, "R"}} {
		t.Run(tc.screen, func(t *testing.T) {
			c, w := cascadeMovesCore(t, 2, 3, true)
			w.AddFloating(9, 800, 600)
			w.SetFullscreen(9, true)
			c.applyAction(tc.a)
			dst := screenWorkspace(c, tc.screen)
			wantFocus(t, c, tc.screen, 9)
			wantColumns(t, w, 1, 2)
			if dst.isFloat(9) || dst.columnOf(9) < 0 || dst.fullscreen != 9 || w.fullscreen != 0 {
				t.Fatalf("destination %+v", dst)
			}
		})
	}
}

func TestUserFloatedToggleFullscreenPromotes(t *testing.T) {
	_, w := cascadeMovesCore(t, 2, 3, false)
	w.ToggleFloating()
	if !w.isFloat(2) {
		t.Fatal("setup not floating")
	}
	w.ToggleFullscreen()
	if w.isFloat(2) || w.fullscreen != 2 {
		t.Fatal("user float not promoted")
	}
	wantBands(t, w, []WindowID{1}, []WindowID{2})
	w.SetFullscreen(2, true)
	w.SetFullscreen(2, true)
	wantBands(t, w, []WindowID{1}, []WindowID{2})
}
