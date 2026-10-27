package core

// receive adds a column moved in from another workspace and focuses it: at
// index at, or where a new window goes when at < 0. A fullscreen window
// there is left, so the moved window is seen.
func (w *Workspace) receive(col Column, at int) {
	id := col.Windows[col.Focus]
	switch {
	case at < 0:
		w.addColumn(col)
	default:
		w.insertColumn(at, col)
	}
	w.Activate(id)
}

// moveFloat moves float id from workspace cur to to, as MoveToWorkspace
// does, focuses it there and keeps it fullscreen.
func moveFloat(cur, to *Workspace, id WindowID) {
	fl := cur.Floats[cur.floatIndex(id)]
	if fl.free && fl.back != nil {
		fl = fl.rehome()
	}
	cur.RemoveWindow(id)
	to.AddFloating(id, fl.W, fl.H)
	to.Floats[len(to.Floats)-1] = fl
	to.Activate(id)
	to.SetFullscreen(id, true)
}
