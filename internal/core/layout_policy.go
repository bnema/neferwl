package core

// layoutAxis is a logical navigation axis, independent of rendering.
type layoutAxis uint8

const (
	verticalAxis layoutAxis = iota
	horizontalAxis
)

func (a layoutAxis) span(r Rect) int {
	if a == verticalAxis {
		return r.H
	}
	return r.W
}
func (a layoutAxis) offset(r *Rect, pixels int) {
	if a == verticalAxis {
		r.Y += pixels
	} else {
		r.X += pixels
	}
}

// layoutPolicy keeps geometry capabilities distinct from workspace navigation.
// Window state remains owned by Workspace; policies contain no mutable state.
type layoutPolicy struct {
	content, workspace layoutAxis
	equalCells, wraps  bool
}

func (w *Workspace) policy() layoutPolicy {
	if w.Overflow == OverflowCascade {
		return layoutPolicy{content: verticalAxis, workspace: horizontalAxis, equalCells: true, wraps: true}
	}
	if w.Overflow == OverflowFixed {
		return layoutPolicy{content: horizontalAxis, workspace: verticalAxis, equalCells: true}
	}
	return layoutPolicy{content: horizontalAxis, workspace: verticalAxis}
}
