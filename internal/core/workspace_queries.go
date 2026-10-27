package core

// isWorkspaceFloat reports a native or stashed window using public queries.
func isWorkspaceFloat(w *Workspace, id WindowID) bool {
	_, _, ok := w.FloatAt(id)
	return ok || w.StashIndex(id) >= 0
}

// workspaceSlidable reports whether the content view accepts a swipe.
func workspaceSlidable(w *Workspace) bool {
	return w.Engine().Scrolls() && len(w.Columns) > 0 && w.Fullscreen == 0 && !w.OnFloat()
}

// workspaceSwipeScale converts touchpad movement to content-axis pixels.
func workspaceSwipeScale(w *Workspace) float64 {
	content, _ := w.Engine().Axes()
	return float64(content.Span(w.Usable)) / viewSwipeMovement
}
