package core

import (
	"math"
	"time"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/core/motion"
)

// retarget keeps the view where it is on screen when View moved from
// before during a landing slide: the slide heads for the new view.
// Without a landing slide the view moves at once, as it always has.
func (w *Workspace) retarget(before int) {
	if w.View == before || !w.view.Motion.On {
		return
	}
	w.view.Off += float64(before - w.View)
	w.view.Motion = motion.NewMotion(motion.ViewSpring(w.view.Off, w.view.Motion.Velocity()), time.Time{}, w.view.Motion.Slow)
}

func (w *Workspace) shiftPixels() int { return int(math.Round(w.view.Off)) }

// scrollsRow reports whether the columns scroll along their row: a
// horizontal content axis that can slide.
func (w *Workspace) scrollsRow() bool {
	content, _ := w.Engine().Axes()
	return w.Engine().Scrolls() && content == layout.Horizontal
}

// snapFocus is the column to focus at view: the current one while it stays
// fully shown at the current view, else the furthest fully shown column
// toward the swipe (forward is rightward).
func (w *Workspace) snapFocus(view int, forward bool) (focus int) {
	return w.Engine().SnapFocus(w.Snapshot(), view, forward)
}
