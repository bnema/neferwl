package core

import (
	"testing"

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
					if !c.edgeFocus(horizontalAxis, 1, tc.d) {
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
		}
	}
}

func TestCrossScreenFocusEntryIgnoresOffscreenColumns(t *testing.T) {
	c, clock := flashCore(t)
	c.addScreen(ports.OutputInfo{Name: "B", Width: 400, Height: 300})
	b := c.screens[1]
	w := b.mon.Current()
	for id := WindowID(4); id <= 10; id++ {
		c.windows.mapped(ports.WindowMapped{ID: id}, clock.now)
		b.mon.AddWindow(id)
	}
	// Leave focus at the right end, so the earlier columns are off-screen.
	w.view.stop()
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
}

func TestCrossScreenNoVisibleWindowPreservesFocus(t *testing.T) {
	c, clock := flashCore(t)
	c.addScreen(ports.OutputInfo{Name: "B", Width: 400, Height: 300})
	b := c.screens[1]
	w := b.mon.Current()
	c.windows.mapped(ports.WindowMapped{ID: 4}, clock.now)
	b.mon.AddWindow(4)
	w.view.off = 2000
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
}

func TestFocusEntryPreservesOverview(t *testing.T) {
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
}

func TestFocusEntryDuringWorkspaceSlide(t *testing.T) {
	m := overviewMonitor()
	m.Focus(1)
	m.AddWindow(5)
	m.Current().FocusID(5)
	m.switchAxis = verticalAxis
	m.switchView.off = -.5
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
}

func TestFocusEntryPanelUsesCommonFrameEdge(t *testing.T) {
	c, _ := flashCore(t)
	c.setLayers([]ports.LayerSurface{{ID: 90, Layer: ports.LayerTop, Anchor: ports.AnchorTop | ports.AnchorLeft | ports.AnchorRight, Height: 50, ExclusiveZone: 50}})
	m := c.cur().mon
	w := m.Current()
	// The tile reaches the bottom frame edge; the float stops above it.
	w.addFloat(Float{ID: 4, W: 40, H: 40, free: true, cx: .5, cy: .8}, true)
	view := m.viewport()
	if view.usable.H >= view.frame.H {
		t.Fatal("setup: missing exclusive panel")
	}
	var want WindowID
	for _, p := range m.Layout() {
		if !p.Floating && view.shows(p) {
			want = p.ID
			break
		}
	}
	if want == 0 {
		t.Fatal("setup: no visible tile")
	}
	m.focusEntry(dirUp)
	id, _ := w.Focused()
	if id != want {
		t.Fatalf("focus %d want tile %d", id, want)
	}
}
