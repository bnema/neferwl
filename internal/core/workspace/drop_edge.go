package workspace

import (
	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/domain"
)

// workspaceEdge reports whether side lies on the workspace-step axis of
// w's layout: left/right for a horizontal one, top/bottom otherwise.
func (w *Workspace) workspaceEdge(side domain.DropSide) bool {
	_, ws := w.Engine().Axes()
	horizontal := side == domain.DropSideLeft || side == domain.DropSideRight
	return horizontal == (ws == layout.Horizontal)
}

// EdgeAction resolves edge action a on side for w's layout. auto switches
// workspaces on the workspace axis and scrolls on the other edges of a
// scrolling layout. An explicit workspace off that axis, or scroll off the
// content edges of a scrolling layout, is invalid: DropNone.
func (w *Workspace) EdgeAction(side domain.DropSide, a domain.DropAction) domain.DropAction {
	if side == domain.DropSideCenter {
		return domain.DropNone
	}
	onWorkspace := w.workspaceEdge(side)
	scrolls := !onWorkspace && w.Engine().Scrolls()
	switch a {
	case domain.DropAuto:
		if onWorkspace {
			return domain.DropWorkspace
		}
		if scrolls {
			return domain.DropScroll
		}
		return domain.DropNone
	case domain.DropWorkspace:
		if !onWorkspace {
			return domain.DropNone
		}
	case domain.DropScroll:
		if !scrolls {
			return domain.DropNone
		}
	}
	return a
}

// dropFrame is where edge zones live: the output, within the usable area.
func (w *Workspace) dropFrame() Rect {
	o, u := w.Output, w.Usable
	x0, y0 := max(o.X, u.X), max(o.Y, u.Y)
	x1, y1 := min(o.X+o.W, u.X+u.W), min(o.Y+o.H, u.Y+u.H)
	return Rect{X: x0, Y: y0, W: max(x1-x0, 0), H: max(y1-y0, 0)}
}

// edgeAnchor is the edge zone of side under p, if it has a valid action.
func (w *Workspace) edgeAnchor(d dropSource, side domain.DropSide, p domain.DropPolicy) (Anchor, bool) {
	a := w.EdgeAction(side, p.Edge[side])
	if a == domain.DropNone {
		return Anchor{}, false
	}
	f := w.dropFrame()
	b := min(edgeBand, f.W/2, f.H/2)
	an := Anchor{Kind: AnchorEdge, Side: side, Action: a, Dir: 1, Rect: f}
	switch side {
	case domain.DropSideLeft:
		an.Dir, an.Rect.W = -1, b
	case domain.DropSideRight:
		an.Rect.X, an.Rect.W = f.X+f.W-b, b
	case domain.DropSideTop:
		an.Dir, an.Rect.H = -1, b
	case domain.DropSideBottom:
		an.Rect.Y, an.Rect.H = f.Y+f.H-b, b
	}
	if !w.valid(d, an) {
		return Anchor{}, false
	}
	return an, true
}

// edgeAt is the edge zone holding x, y: in a corner the shallower edge
// wins, and left/right on a tie.
func (w *Workspace) edgeAt(d dropSource, x, y int, p domain.DropPolicy) (Anchor, bool) {
	f := w.dropFrame()
	if !contains(f, x, y) {
		return Anchor{}, false
	}
	var best Anchor
	depth, found := 0, false
	for s := domain.DropSideLeft; s <= domain.DropSideBottom; s++ {
		var dd int
		switch s {
		case domain.DropSideLeft:
			dd = x - f.X
		case domain.DropSideRight:
			dd = f.X + f.W - 1 - x
		case domain.DropSideTop:
			dd = y - f.Y
		default:
			dd = f.Y + f.H - 1 - y
		}
		// Sides are tried left, right, top, bottom: a strict < keeps
		// left/right on a tie.
		if dd >= edgeBand || found && dd >= depth {
			continue
		}
		if a, ok := w.edgeAnchor(d, s, p); ok && contains(a.Rect, x, y) {
			best, depth, found = a, dd, true
		}
	}
	return best, found
}
