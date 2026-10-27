package workspace

import (
	"math"
	"time"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/core/motion"
)

// retarget keeps the view where it is on screen when View moved from
// before during a landing slide: the slide heads for the new view.
// Without a landing slide the view moves at once.
func (w *Workspace) retarget(before int) {
	if w.View == before || !w.view.Motion.On {
		return
	}
	w.view.Off += float64(before - w.View)
	w.view.Motion = motion.NewMotion(motion.ViewSpring(w.view.Off, w.view.Motion.Velocity()), time.Time{}, w.view.Motion.Slow)
}

// viewSwipeMovement is the touchpad distance that scrolls the columns by one
// usable width.
const viewSwipeMovement = 1200.0

// Slidable reports whether a swipe can scroll the columns: scroll or cascade
// overflow, with the focus on the columns and no fullscreen window.
func (w *Workspace) Slidable() bool {
	return w.Engine().Scrolls() && len(w.Columns) > 0 && w.Fullscreen == 0 && !w.OnFloat()
}

// SwipeScale turns touchpad distance into content-axis pixels:
// viewSwipeMovement scrolls one usable width.
func (w *Workspace) SwipeScale() float64 {
	content, _ := w.Engine().Axes()
	return float64(content.Span(w.Usable)) / viewSwipeMovement
}

// SnapPoints are the sorted views that align a column edge with the usable
// area, never past the first or last column. They are built in dst's
// storage, which may be nil.
func (w *Workspace) SnapPoints(dst []float64) []float64 {
	return w.Engine().SnapPoints(w.Snapshot(), dst[:0])
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
