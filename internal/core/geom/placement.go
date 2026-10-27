package geom

import "github.com/bnema/neferwl/internal/domain"

// Placement describes a window and its presentation geometry.
type Placement struct {
	// ID is the window the placement is for.
	ID domain.WindowID
	// Rect is the window's rectangle in output-local logical pixels.
	Rect domain.Rect
	// Fullscreen marks the window as the workspace's fullscreen window.
	Fullscreen bool
	// Focused marks the window that has the keyboard focus.
	Focused bool
	// Hidden marks a window that is not drawn or reachable by input.
	Hidden bool
	// Floating windows sit at their own size. Below marks a native float
	// ordered behind the columns (never a stashed window).
	Floating, Below bool
	// Peek is a stashed window peeking in beside the selected one, or an
	// overview preview of a neighbor workspace: dimmed.
	Peek bool
	// Card marks a stash card of the overview's pile (as opposed to a tile
	// or a covering float): the selection outline goes around a selected
	// one and stays off the ones it would cross.
	Card bool
	// Veil is the share (0..1) of the peek veil (Stash.Dim) the window
	// draws: 1 for a settled peek, less while a stash slide brings it to
	// the centre. Set wherever Peek is, and on a sliding stash selection.
	Veil float64
	// Preview is the scale of an overview preview: its buffer is drawn
	// that much smaller in Rect, the client keeps its size. 0 otherwise.
	Preview float64
	// Neighbors are the sides touching a tile visible in the usable area.
	// Inset reserves room for drawn lines: all sides of a float; all sides
	// of a tile with gaps; only the right and bottom shared sides of a tile
	// without gaps.
	Neighbors, Inset domain.Sides
	// Leaving marks a window that closed or hid but is still drawn while
	// it fades out (transition.go). It stays Hidden, so input, focus,
	// popups and configures ignore it; only the scene shows it.
	Leaving bool
	// Fade, Dim and Zoom are set on shown layouts only (refreshShown),
	// animated: the window's fade (0 opaque, 1 invisible), a veil opacity
	// added to the one Veil draws (0..1), and the content zoom as drawn
	// when a scale motion runs (Preview times the drawn/settled width, 1
	// when the content is drawn at its size; 0 without one). Preview
	// itself stays the settled one: input, popups and the renderer's paint
	// order read it to tell a card from a window.
	Fade, Dim, Zoom float64
}

// Peeking marks p a peek drawing share of the peek veil (Stash.Dim).
func (p *Placement) Peeking(veil float64) { p.Peek, p.Veil = true, veil }
