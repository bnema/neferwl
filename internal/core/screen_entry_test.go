package core

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/ports"
)

func TestCrossScreenFocusEntry(t *testing.T) {
	for _, tc := range []struct {
		action Action
		d      direction
		x, y   int
		want   WindowID
	}{
		{ActionFocusMonitorLeft, dirLeft, -400, 0, 5},
		{ActionFocusMonitorRight, dirRight, 300, 0, 4},
		{ActionFocusMonitorUp, dirUp, 0, -300, 7},
		{ActionFocusMonitorDown, dirDown, 0, 200, 6},
	} {
		for _, edge := range []bool{false, true} {
			t.Run(string(tc.action)+"/edge="+map[bool]string{false: "false", true: "true"}[edge], func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					c, clock := flashCore(t)
					c.addScreen(ports.OutputInfo{Name: "B", Width: 400, Height: 300})
					b := c.screens[1]
					b.x, b.y = tc.x, tc.y
					w := b.mon.Current()
					for i, pos := range [][2]float64{{.1, .5}, {.9, .5}, {.5, .1}, {.5, .9}} {
						id := WindowID(i + 4)
						c.windows.mapped(ports.WindowMapped{ID: id}, clock.now)
						w.addFloat(Float{ID: id, W: 40, H: 40, free: true, cx: pos[0], cy: pos[1]}, true)
					}
					w.FocusID(4)
					if edge && (tc.d == dirLeft || tc.d == dirRight) {
						if !c.edgeFocus(Horizontal, 1, tc.d) {
							t.Fatal("did not cross")
						}
					} else {
						c.applyAction(tc.action)
					}
					id, _ := b.mon.Focused()
					if c.cur() != b || id != tc.want {
						t.Fatalf("screen %s focus %d want %d", c.cur().name(), id, tc.want)
					}
					if !edge && (c.flash.pending == nil || *c.flash.pending != (flashTarget{level: flashWindow, window: tc.want, output: "B"})) {
						t.Fatalf("flash %+v", c.flash.pending)
					}

				})
			})
		}
	}
}

func TestCrossScreenFocusEntryIgnoresOffscreenColumns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock := flashCore(t)
		c.addScreen(ports.OutputInfo{Name: "B", Width: 400, Height: 300})
		b := c.screens[1]
		w := b.mon.Current()
		for id := WindowID(4); id <= 10; id++ {
			c.windows.mapped(ports.WindowMapped{ID: id}, clock.now)
			b.mon.AddWindow(id)
		}
		// Leave focus at the right end, so the earlier columns are off-screen.
		w.view.Stop()
		view := b.mon.viewport()
		var want WindowID
		best := 0
		offscreen := false
		for _, p := range b.mon.Layout() {
			if !view.shows(p) {
				offscreen = true
				continue
			}
			if want == 0 || p.Rect.X < best {
				want, best = p.ID, p.Rect.X
			}
		}
		if !offscreen || want == 0 {
			t.Fatal("setup: need both visible and off-screen columns")
		}
		c.applyAction(ActionFocusMonitorRight)
		id, _ := b.mon.Focused()
		if id != want {
			t.Fatalf("focus %d want visible leftmost %d", id, want)
		}

	})
}

func TestCrossScreenNoVisibleWindowPreservesFocus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock := flashCore(t)
		c.addScreen(ports.OutputInfo{Name: "B", Width: 400, Height: 300})
		b := c.screens[1]
		w := b.mon.Current()
		c.windows.mapped(ports.WindowMapped{ID: 4}, clock.now)
		b.mon.AddWindow(4)
		w.view.Off = 2000
		for _, p := range b.mon.Layout() {
			if b.mon.viewport().shows(p) {
				t.Fatal("setup: visible window")
			}
		}
		c.applyAction(ActionFocusMonitorRight)
		id, _ := b.mon.Focused()
		if c.cur() != b || id != 4 || c.flash.pending != nil {
			t.Fatalf("focus %d flash %+v", id, c.flash.pending)
		}

	})
}

func TestFocusEntryPreservesOverview(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := overviewMonitor()
		m.ToggleOverview()
		m.OverviewMove(-1, 0)
		selected, at := m.ov.selected, m.ov.selectedAt
		focused, _ := m.Current().Focused()
		for _, d := range []direction{dirLeft, dirRight, dirUp, dirDown} {
			m.focusEntry(d)
			got, _ := m.Current().Focused()
			if !m.ov.open || m.ov.selected != selected || m.ov.selectedAt != at || got != focused {
				t.Fatalf("overview selection or focus changed: selected %d focus %d", m.ov.selected, got)
			}
		}

	})
}

func TestFocusEntryDuringWorkspaceSlide(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := overviewMonitor()
		m.Focus(1)
		m.AddWindow(5)
		m.Current().FocusID(5)
		m.switchAxis = Vertical
		m.switchView.Off = -.5
		view := m.viewport()
		outgoing := false
		for _, p := range m.Layout() {
			if !m.Current().has(p.ID) && view.shows(p) {
				outgoing = true
			}
		}
		if !outgoing {
			t.Fatal("setup: outgoing workspace must be visible")
		}
		m.focusEntry(dirDown)
		id, _ := m.Current().Focused()
		if id != 4 {
			t.Fatalf("focus %d want current workspace window 4", id)
		}

	})
}

func TestFocusEntryPanelUsesCommonFrameEdge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := flashCore(t)
		c.setLayers([]ports.LayerSurface{{ID: 90, Layer: ports.LayerTop, Anchor: ports.AnchorTop | ports.AnchorLeft | ports.AnchorRight, Height: 50, ExclusiveZone: 50}})
		m := c.cur().mon
		w := m.Current()
		// Entering from above: the float sits above the tiles, which start a gap
		// below the panel. Measured from their own areas (frame for the float,
		// usable for tiles) instead of the frame edge, the tile would win.
		w.Gaps = 20
		w.addFloat(Float{ID: 4, W: 40, H: 40, free: true, cx: .5, cy: 0}, true)
		view := m.viewport()
		if view.usable.Y <= view.frame.Y {
			t.Fatal("setup: missing exclusive top panel")
		}
		var float, tile Rect
		for _, p := range m.Layout() {
			switch {
			case p.ID == 4:
				float = p.Rect
			case !p.Floating && view.shows(p) && tile == (Rect{}):
				tile = p.Rect
			}
		}
		if tile == (Rect{}) || float.Y >= tile.Y || float.Y-view.frame.Y <= tile.Y-view.usable.Y {
			t.Fatalf("setup: float %v tile %v view %+v do not separate frame and area distances", float, tile, view)
		}
		m.focusEntry(dirDown)
		id, _ := w.Focused()
		if id != 4 {
			t.Fatalf("focus %d want float 4", id)
		}

	})
}

func TestCrossScreenFocusEntryResolvesModal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock := flashCore(t)
		c.addScreen(ports.OutputInfo{Name: "B", Width: 400, Height: 300})
		b := c.screens[1]
		w := b.mon.Current()
		for _, id := range []WindowID{4, 5, 6} {
			c.windows.mapped(ports.WindowMapped{ID: id}, clock.now)
		}
		w.addFloat(Float{ID: 4, W: 40, H: 40, free: true, cx: .1, cy: .5}, true)
		w.AddDialog(5, 4, 40, 40)
		w.SetModal(5, true)
		w.addFloat(Float{ID: 6, W: 40, H: 40, free: true, cx: .9, cy: .5}, true)
		c.applyAction(ActionFocusMonitorRight)
		if id, _ := b.mon.Focused(); c.cur() != b || id != 5 {
			t.Fatalf("focus %d, want modal 5", id)
		}

	})
}

func TestCrossScreenFocusEntrySkipsDemotedFloat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock := flashCore(t)
		c.addScreen(ports.OutputInfo{Name: "B", Width: 400, Height: 300})
		b := c.screens[1]
		w := b.mon.Current()
		w.Gaps = 0
		for _, id := range []WindowID{4, 5} {
			c.windows.mapped(ports.WindowMapped{ID: id}, clock.now)
		}
		b.mon.AddWindow(4)
		w.AddFloating(5, 400, 300)
		w.FocusID(4)
		if !w.Floats[0].below {
			t.Fatal("setup: float not demoted")
		}
		c.applyAction(ActionFocusMonitorRight)
		if id, _ := b.mon.Focused(); c.cur() != b || id != 4 || !w.Floats[0].below {
			t.Fatalf("focus %d, floats %+v", id, w.Floats)
		}

	})
}
