package workspace

// ApplyFree runs a move or resize action on the focused free float and
// reports whether it did: moves shift it by freeNudge, resizes change its
// size by a share of the usable area.
func (w *Workspace) ApplyFree(a Action) bool {
	i := w.focusedFree()
	if i < 0 {
		return false
	}
	if axis, step, ok := ResizeArg(a); ok {
		w.resizeFree(i, axis == ResizeWidth, step)
		return true
	}
	dx, dy := 0, 0
	switch a {
	case ActionMoveColumnLeft:
		dx = -freeNudge
	case ActionMoveColumnRight:
		dx = freeNudge
	case ActionMoveWindowUp:
		dy = -freeNudge
	case ActionMoveWindowDown:
		dy = freeNudge
	default:
		return false
	}
	w.nudgeFree(i, dx, dy)
	return true
}
