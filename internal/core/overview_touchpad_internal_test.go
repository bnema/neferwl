package core

import (
	"context"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestOverviewTwoFingerDoesNotNavigate(t *testing.T) {
	c, _, sc, _ := stashRig(t, true)
	m := sc.mon
	m.ToggleOverview()
	w, card := m.Current(), m.cardAt(m.Current())
	for _, axis := range []ports.PointerAxis{
		{Source: ports.AxisFinger, Horizontal: ports.ScrollAxis{Set: true, Value: 1000}},
		{Source: ports.AxisFinger, Vertical: ports.ScrollAxis{Set: true, Value: 1000}},
		{Source: ports.AxisFinger, Vertical: ports.ScrollAxis{Stop: true}},
	} {
		if err := c.handleInput(context.Background(), axis); err != nil {
			t.Fatal(err)
		}
	}
	if m.Current() != w || m.cardAt(w) != card {
		t.Fatal("two-finger scroll navigated the overview")
	}
}

func TestOverviewSwipeCancellationResetsDistance(t *testing.T) {
	c, _, sc, _ := stashRig(t, true)
	m := sc.mon
	m.ToggleOverview()
	w := m.Current()
	c.swipeBegin(ports.SwipeBegin{Fingers: 3})
	c.swipeUpdate(ports.SwipeUpdate{DY: -40})
	c.swipeEnd(ports.SwipeEnd{Cancelled: true})
	c.swipeBegin(ports.SwipeBegin{Fingers: 3})
	c.swipeUpdate(ports.SwipeUpdate{DY: -40})
	if m.cardAt(w) != 2 {
		t.Fatal("cancelled gesture left accumulated distance")
	}
	c.swipeUpdate(ports.SwipeUpdate{DY: -30})
	if m.cardAt(w) != 1 {
		t.Fatal("selection did not change during movement")
	}
	c.swipeEnd(ports.SwipeEnd{Cancelled: true})
	if m.cardAt(w) != 1 {
		t.Fatal("cancellation undid an already selected card")
	}
}
