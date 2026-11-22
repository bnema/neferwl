package core_test

import (
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// tabbedDragRig shows a tabbed column {1, 2, 3} (3 visible) left of 4.
func tabbedDragRig(t *testing.T) (*dragRig, []domain.Scene) {
	t.Helper()
	r := startDragRig(t, func(c *domain.Config) { c.Layout.MaxColumns = 3 }, dragOut)
	key := func(sym string) {
		r.in(ports.KeyEvent{Keysym: sym, Mods: domain.ModSuper, Pressed: true})
		r.in(ports.KeyEvent{Keysym: sym, Mods: domain.ModSuper})
		r.settle(t)
	}
	r.client <- ports.WindowMapped{ID: 1}
	r.settle(t)
	r.client <- ports.WindowMapped{ID: 2}
	r.settle(t)
	key("bracketleft")
	r.client <- ports.WindowMapped{ID: 3}
	r.settle(t)
	key("bracketleft")
	r.client <- ports.WindowMapped{ID: 4}
	r.settle(t)
	key("h")
	key("t")
	r.in(ports.KeyEvent{Keysym: "Super_L"})
	set := r.settle(t)
	if got := columnsOf(set, "A"); !slices.EqualFunc(got, [][]domain.WindowID{{3}, {4}}, slices.Equal) || len(dragScene(set, "A").TabBars) != 1 {
		t.Fatalf("setup: columns %v bars %+v", got, dragScene(set, "A").TabBars)
	}
	return r, set
}

func tabRect(t *testing.T, set []domain.Scene, id domain.WindowID) domain.Rect {
	t.Helper()
	for _, tab := range dragScene(set, "A").TabBars[0].Tabs {
		if tab.Window == id {
			return tab.Rect
		}
	}
	t.Fatalf("no tab %d", id)
	return domain.Rect{}
}

// buttonsSent drains the commands and counts the button events clients got.
func buttonsSent(r *dragRig) int {
	n := 0
	for len(r.commands) > 0 {
		if _, ok := (<-r.commands).(ports.PointerButtonTo); ok {
			n++
		}
	}
	return n
}

func TestTabClickSelects(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, set := tabbedDragRig(t)
		tab := tabRect(t, set, 2)
		r.moveTo(float64(tab.X+tab.W/2), float64(tab.Y+tab.H/2), 1)
		r.settle(t)
		buttonsSent(r)
		r.in(ports.PointerButton{Button: 0x110, Pressed: true})
		set = r.settle(t)
		r.in(ports.PointerButton{Button: 0x110})
		r.settle(t)
		if n := buttonsSent(r); n != 0 {
			t.Fatalf("%d button events reached a client", n)
		}
		if got := columnsOf(set, "A"); !slices.EqualFunc(got, [][]domain.WindowID{{2}, {4}}, slices.Equal) {
			t.Fatalf("columns %v", got)
		}
		if w, _ := windowIn(set, "A", 2); !w.Focused {
			t.Fatal("tab 2 not focused")
		}
		// A right press does nothing.
		tab = tabRect(t, set, 1)
		r.moveTo(float64(tab.X+tab.W/2), float64(tab.Y+tab.H/2), 2)
		r.settle(t)
		r.in(ports.PointerButton{Button: 0x111, Pressed: true})
		r.in(ports.PointerButton{Button: 0x111})
		set = r.settle(t)
		if n := buttonsSent(r); n != 0 {
			t.Fatalf("%d right button events reached a client", n)
		}
		if got := columnsOf(set, "A"); !slices.EqualFunc(got, [][]domain.WindowID{{2}, {4}}, slices.Equal) {
			t.Fatalf("right press changed columns %v", got)
		}
	})
}

func TestTabCmdDragMovesHiddenTab(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, set := tabbedDragRig(t)
		tab := tabRect(t, set, 1)
		four := dragRect(t, set, "A", 4)
		r.moveTo(float64(tab.X+tab.W/2), float64(tab.Y+tab.H/2), 1)
		r.settle(t)
		r.press(t, 0x110, domain.ModSuper)
		r.settle(t)
		r.moveTo(float64(four.X+four.W-80), float64(four.Y+four.H/2), 2)
		if p := dragScene(r.settle(t), "A").DropPreview; p == nil || p.Label != domain.DropColumn.Label() {
			t.Fatalf("no column zone: %+v", p)
		}
		r.in(ports.PointerButton{Button: 0x110})
		r.in(ports.KeyEvent{Keysym: "Super_L"})
		set = r.settle(t)
		if got := columnsOf(set, "A"); len(got) != 3 || !slices.Equal(got[2], []domain.WindowID{1}) {
			t.Fatalf("columns %v", got)
		}
		bars := dragScene(set, "A").TabBars
		if len(bars) != 1 || len(bars[0].Tabs) != 2 {
			t.Fatalf("source bars %+v", bars)
		}
	})
}

// A tab click closes an open grabbing popup, as a click on a window does.
func TestTabClickDismissesPopupGrab(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, set := tabbedDragRig(t)
		pos := domain.Positioner{Width: 10, Height: 10, AnchorRect: domain.Rect{W: 1, H: 1}, Anchor: ports.EdgeBottomRight, Gravity: ports.EdgeBottomRight}
		r.client <- ports.PopupRequest{ID: 10, Parent: 3, Positioner: pos, Grab: true}
		r.settle(t)
		r.client <- ports.PopupMapped{ID: 10}
		r.settle(t)
		// A plain click: no modifier left from the setup binds.
		r.in(ports.KeyEvent{Keysym: "Shift_L", Mods: domain.ModShift, Pressed: true})
		r.in(ports.KeyEvent{Keysym: "Shift_L"})
		r.settle(t)
		r.drain()
		// The visible tab's own tab: focus does not move.
		tab := tabRect(t, set, 3)
		r.moveTo(float64(tab.X+tab.W/2), float64(tab.Y+tab.H/2), 1)
		r.in(ports.PointerButton{Button: 0x110, Pressed: true})
		r.in(ports.PointerButton{Button: 0x110})
		r.settle(t)
		closed := false
		for _, c := range r.drain() {
			if v, ok := c.(ports.ClosePopup); ok && v.ID == 10 {
				closed = true
			}
		}
		if !closed {
			t.Fatal("popup grab not dismissed")
		}
	})
}
