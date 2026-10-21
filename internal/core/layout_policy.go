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

// validSpans requires a complete, exclusive lane stamp: every column has
// a positive span and the same nonzero identity, with no members elsewhere
// in the workspace. Equal-cell lanes must also sum to 10000 basis points.
func (w *Workspace) validSpans(first, last int) bool {
	if first > last || w.Overflow == OverflowFixed && !w.laneRow() {
		return false
	}
	stamp := w.Columns[first].spanStamp
	if stamp == 0 {
		return false
	}
	total := 0
	for i := first; i <= last; i++ {
		c := w.Columns[i]
		if c.Span <= 0 || c.spanStamp != stamp {
			return false
		}
		total += c.Span
	}
	count := 0
	for _, c := range w.Columns {
		if c.spanStamp == stamp {
			count++
		}
	}
	return count == last-first+1 && (!w.policy().equalCells || total == 10000)
}

// dropStaleSpans runs before scroll measures mutated layout geometry. Decide
// validity before clearing any stamps, so shared identities cannot become
// valid merely because an earlier invalid lane was cleared.
func (w *Workspace) dropStaleSpans() {
	var stale []int
	for first := 0; first < len(w.Columns); {
		_, last := w.laneBounds(w.laneOf(first))
		if !w.validSpans(first, last) {
			for i := first; i <= last; i++ {
				stale = append(stale, i)
			}
		}
		first = last + 1
	}
	for _, i := range stale {
		w.Columns[i].Span, w.Columns[i].spanStamp = 0, 0
	}
}

func (w *Workspace) clearSpans(col int) {
	if col < 0 || col >= len(w.Columns) {
		return
	}
	first, last := w.laneBounds(w.laneOf(col))
	for i := first; i <= last; i++ {
		w.Columns[i].Span = 0
		w.Columns[i].spanStamp = 0
	}
}
