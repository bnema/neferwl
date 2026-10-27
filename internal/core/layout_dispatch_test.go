package core

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/stretchr/testify/mock"
)

// dispatchTraits are the constant answers of a layout double.
type dispatchTraits struct {
	scrolls, spiral, laneRow bool
	content, workspace       layout.Axis
}

// scrollTraits are the neutral scroll layout's.
var scrollTraits = dispatchTraits{scrolls: true, content: layout.Horizontal, workspace: layout.Vertical}

// The position hint counts a workspace's rows from the layout's lane count.
func TestPositionHintLaneCountDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, l := dispatchLayoutWith(t, 0, dispatchTraits{scrolls: true, content: layout.Vertical, workspace: layout.Horizontal})
		m := newMonitor("m", "")
		w := m.Current()
		w.Override, w.Usable = l, Rect{W: 900, H: 600}
		l.EXPECT().LaneCount(mock.Anything).Return(3).Once()
		l.EXPECT().LaneOf(mock.Anything, 0).Return(1).Once()
		h := positionHintGrid(m)
		if h.Counts[0] != 3 || h.ActiveRow != 1 {
			t.Fatalf("hint rows %d, active row %d: want the layout's 3 lanes and lane 1", h.Counts[0], h.ActiveRow)
		}

	})
}

// A swipe scrolls the columns only in a layout whose content axis slides.
func TestSwipeTraitDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, scrolls := range []bool{true, false} {
			c := layoutCore(0, tile{"M", 0, 0, 900, 600})
			c.cfg.Animations.On = true
			sc := c.screens[0]
			w := sc.mon.Current()
			_, l := dispatchLayoutWith(t, 0, dispatchTraits{scrolls: scrolls, content: layout.Horizontal, workspace: layout.Vertical})
			l.EXPECT().SnapPoints(mock.Anything, mock.Anything).Return([]float64{0, 400}).Maybe()
			w.Override, w.Usable = l, Rect{W: 900, H: 600}
			w.Columns = []Column{{Windows: []WindowID{1}}, {Windows: []WindowID{2}}}
			g := &swipeGesture{screen: sc, fingers: 3, cx: 40}
			c.decide(g)
			if got := g.mode == swipeColumns; got != scrolls {
				t.Fatalf("scrolls = %v: swipe mode %v", scrolls, g.mode)
			}
		}

	})
}

// previewLanes makes the overview read a tile's lane from its Y: the tile one
// Usable.H down sits in another lane, dimmed, instead of in the focused one.
