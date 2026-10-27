package workspace

import (
	"time"

	"github.com/bnema/neferwl/internal/core/motion"
)

// ColumnsPosition is the last drawn column view, in logical pixels.
func (w *Workspace) ColumnsPosition() float64 { return float64(w.View) + w.view.Off }

// StashPosition is the last drawn stash selection, in stash windows.
func (w *Workspace) StashPosition() float64 { return float64(w.stashAt) + w.stashView.Off }

// SampleColumns returns the last drawn view and its velocity at now without
// advancing presentation or changing the motion's bookkeeping.
func (w *Workspace) SampleColumns(now time.Time) (pos, velocity float64) {
	pos = w.ColumnsPosition()
	if w.view.Motion.On {
		_, velocity = w.view.Motion.SampleAt(now)
	}
	return
}

// SampleStash returns the last drawn selection and its velocity at now without
// advancing presentation or changing the motion's bookkeeping.
func (w *Workspace) SampleStash(now time.Time) (pos, velocity float64) {
	pos = w.StashPosition()
	if w.stashView.Motion.On {
		_, velocity = w.stashView.Motion.SampleAt(now)
	}
	return
}

// DragColumns places the drawn view at pos relative to the settled view.
// It preserves any landing motion; BeginColumnsDrag takes over that motion.
func (w *Workspace) DragColumns(pos float64) { w.view.Off = pos - float64(w.View) }

// DragStash places the drawn selection at pos relative to the settled selection.
// It preserves any landing motion; BeginStashDrag takes over that motion.
func (w *Workspace) DragStash(pos float64) { w.stashView.Off = pos - float64(w.stashAt) }

// BeginColumnsDrag drops the landing motion, keeping the last drawn view.
func (w *Workspace) BeginColumnsDrag() float64 {
	w.view.Motion = motion.Motion{}
	return w.ColumnsPosition()
}

// BeginStashDrag drops the landing motion, keeping the last drawn selection.
func (w *Workspace) BeginStashDrag() float64 {
	w.stashView.Motion = motion.Motion{}
	return w.StashPosition()
}

// LandColumns starts a core-built landing motion over the current drag offset.
func (w *Workspace) LandColumns(m motion.Motion) { w.view.Motion = m }

// LandStash starts a core-built landing motion over the current drag offset.
func (w *Workspace) LandStash(m motion.Motion) { w.stashView.Motion = m }

// StopColumns settles the drawn columns at the workspace's view.
func (w *Workspace) StopColumns() { w.view.Stop() }

// ResetView scrolls the columns back to view 0, leaving any running slide
// alone; pair it with StopColumns to settle the drawn columns there.
func (w *Workspace) ResetView() { w.View = 0 }

// StopStash settles the drawn stash at the selected window.
func (w *Workspace) StopStash() { w.stashView.Stop() }

// StopLandings settles running column and stash springs, leaving drags alone.
func (w *Workspace) StopLandings() {
	if w.view.Motion.On {
		w.StopColumns()
	}
	if w.stashView.Motion.On {
		w.StopStash()
	}
}

// StepSlides advances both landing motions to the core-supplied frame time.
func (w *Workspace) StepSlides(now time.Time) { w.view.Step(now); w.stashView.Step(now) }

// SlidesBusy reports a drag offset or a running landing on either slide.
func (w *Workspace) SlidesBusy() bool { return w.view.Busy() || w.stashView.Busy() }

// SlidesRunning reports a running landing, excluding finger-driven offsets.
func (w *Workspace) SlidesRunning() bool { return w.view.Motion.On || w.stashView.Motion.On }
