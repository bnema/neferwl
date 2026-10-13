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

// maximizeHides reports whether a maximized column hides the others, so
// it is a transient state that focus changes undo: fixed overflow. In
// scroll overflow and cascade it is a lasting state of its column (a
// cascade band packs a maximized column alone).
func (p layoutPolicy) maximizeHides() bool { return p.equalCells && !p.wraps }

// workspaceStep is the workspace step a directional focus past the content
// edge takes along axis: dir when axis is the workspace axis, else 0.
func (p layoutPolicy) workspaceStep(axis layoutAxis, dir int) int {
	if axis == p.workspace {
		return dir
	}
	return 0
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
