package workspace

import (
	"slices"

	"github.com/bnema/neferwl/internal/core/layout"
)

// ruleFloat is the float of a window a rule floats.
func (w *Workspace) ruleFloat(id WindowID, floating bool, width, height int) Float {
	if floating {
		return Float{ID: id, W: width, H: height}
	}
	f := Float{ID: id, free: true, cx: 0.5, cy: 0.5}
	if w.Usable.W > 0 && w.Usable.H > 0 {
		// As makeFree: the size is core's, fixed now. Without an output
		// yet, floatRect's default (half the usable area) applies.
		r := w.floatRect(f)
		f.W, f.H = max(r.W-2*w.border, 1), max(r.H-2*w.border, 1)
	}
	return f
}

// addFloat adds a new float; quiet puts it below the others without the
// float focus, so the focused window stays the same.
func (w *Workspace) addFloat(f Float, quiet bool) {
	if f.ID == 0 || w.Has(f.ID) {
		return
	}
	if quiet {
		// A workspace with nothing else focuses it, so it has a focus when shown.
		w.floatFocus = w.floatFocus || w.Empty()
		w.Floats = slices.Insert(w.Floats, 0, f)
		return
	}
	w.Floats = append(w.Floats, f)
	w.floatFocus = true
}

// addColumnQuiet adds a column where a new window goes, keeping the focus
// where it is (ADR 011 golden rule).
func (w *Workspace) addColumnQuiet(col layout.Column) { w.placeColumn(col, true) }
