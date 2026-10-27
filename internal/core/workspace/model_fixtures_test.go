package workspace

import "testing"

func previewOf(t *testing.T, ps []Placement, id WindowID) Placement {
	t.Helper()
	for _, p := range ps {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no placement for %d", id)
	return Placement{}
}

func threeColumns() *Workspace {
	w := workspace()
	w.SetMaxColumns(3)
	for id := WindowID(1); id <= 3; id++ {
		w.AddWindow(id)
	}
	return w
}

func rc(x, y, w, h int) Rect { return Rect{X: x, Y: y, W: w, H: h} }

func cascadeMovesCore(t *testing.T, n, maxCols int, sides bool) (struct{}, *Workspace) {
	t.Helper()
	w := &Workspace{}
	w.SetOutput(900, 600)
	w.SetGaps(0)
	w.SetMaxColumns(maxCols)
	w.SetOverflow(OverflowCascade)
	for id := WindowID(1); id <= WindowID(n); id++ {
		w.AddWindow(id)
	}
	return struct{}{}, w
}
