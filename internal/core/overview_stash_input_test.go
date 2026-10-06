package core

import (
	"slices"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

func TestOverviewStashInputNavigation(t *testing.T) {
	for _, input := range []string{"swipe", "wheel", "bind"} {
		t.Run(input, func(t *testing.T) {
			c, _, sc, _ := stashRig(t, true)
			m := sc.mon
			w := m.Current()
			before := stashIDs(w)
			m.ToggleOverview()
			var at time.Duration
			move := func(dx, dy int, cancelled bool) {
				t.Helper()
				switch input {
				case "swipe":
					at += time.Second
					c.swipeBegin(ports.SwipeBegin{Fingers: 3, Time: at})
					for range 3 {
						at += 8 * time.Millisecond
						c.swipeUpdate(ports.SwipeUpdate{DX: float64(dx) * 40, DY: float64(dy) * 40, Time: at})
					}
					c.swipeEnd(ports.SwipeEnd{Cancelled: cancelled, Time: at})
				case "wheel":
					m.overviewScroll(ports.PointerAxis{Source: ports.AxisWheel,
						Horizontal: ports.ScrollAxis{Set: dx != 0, Value: float64(dx) * 70, V120: int32(dx * 120)},
						Vertical:   ports.ScrollAxis{Set: dy != 0, Value: float64(dy) * 70, V120: int32(dy * 120)}})
					c.scrollStop(ports.PointerAxis{Vertical: ports.ScrollAxis{Stop: true}})
				case "bind":
					a := ActionFocusWorkspaceDown
					if dy < 0 {
						a = ActionFocusWorkspaceUp
					} else if dx > 0 {
						a = ActionFocusColumnRight
					}
					m.overviewFocus(a)
				}
			}
			move(0, -1, false)
			if m.cardAt(w) != 1 {
				t.Fatalf("up selected card %d, want 1", m.cardAt(w))
			}
			move(0, 1, false)
			move(0, 1, false)
			if m.Current() != w || m.cardAt(w) != 2 || !slices.Equal(stashIDs(w), before) {
				t.Fatal("down escaped or reordered the stash")
			}
			move(1, 0, false)
			if m.cardAt(w) >= 0 {
				t.Fatal("right did not leave the stash")
			}
		})
	}
}
