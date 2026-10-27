package workspace

import (
	"slices"

	"github.com/bnema/neferwl/internal/core/motion"
)

// Inspection is a detached read-only snapshot for diagnostics and tests.
// Slices are copied; editing them never changes the workspace.
type Inspection struct {
	// Recent is the raw MRU history, newest first.
	Recent []WindowID
	// Left is the fullscreen refusal latch.
	Left WindowID
	// FloatFocus and StashFocus retain the underlying focus flags.
	FloatFocus, StashFocus bool
	// StashHidden and StashOver retain stash visibility bookkeeping.
	StashHidden bool
	StashOver   WindowID
	// HiddenFullscreen is the deferred stash fullscreen request.
	HiddenFullscreen WindowID
	// ColumnsSlide and StashSlide are detached presentation values.
	ColumnsSlide, StashSlide motion.Slide
	// Floats records private float placement metadata in native order.
	Floats []FloatInspection
}

// FloatInspection contains detached floating placement metadata.
type FloatInspection struct {
	// ID identifies the floating window.
	ID WindowID
	// Below and Free distinguish demoted native floats and user-owned geometry.
	Below, Free bool
	// CX and CY are the normalized centre within the usable area.
	CX, CY float64
	// Over is the dialog's owner.
	Over WindowID
}

// Inspect returns a read-only snapshot for diagnostics and tests. All slices
// are copied, and slide motions are values, never mutable model accessors.
func (w *Workspace) Inspect() Inspection {
	s := Inspection{Recent: slices.Clone(w.recent), Left: w.left, FloatFocus: w.floatFocus,
		StashFocus: w.stashFocus, StashHidden: w.stashHidden, StashOver: w.stashOver,
		HiddenFullscreen: w.hiddenFullscreen, ColumnsSlide: w.view, StashSlide: w.stashView}
	if w.Floats != nil {
		s.Floats = make([]FloatInspection, len(w.Floats))
		for i, f := range w.Floats {
			s.Floats[i] = FloatInspection{ID: f.ID, Below: f.below, Free: f.free, CX: f.cx, CY: f.cy, Over: f.over}
		}
	}
	return s
}
