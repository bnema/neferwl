package core

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/domain"
)

// Held members are reported fullscreen at their own output-sized rect, the
// workspace names its active cover, and a cover change republishes.
func TestStateFullscreenGroup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := lineageCore(t)
		c.addScreen(domain.OutputInfo{Name: "OUT-1", Width: 300, Height: 200})
		w := c.cur().mon.Current()
		w.SetOverflow(OverflowScroll)
		w.AddWindow(1)
		w.AddWindow(2)
		w.AddWindow(3)
		w.Fullscreen, w.Held = 1, []WindowID{2}
		st := cloneState(c.state())
		full := map[WindowID]bool{}
		for _, v := range st.Windows {
			full[v.ID] = v.Fullscreen
		}
		if !full[1] || !full[2] || full[3] {
			t.Fatalf("fullscreen flags %v", full)
		}
		var ws *domain.WorkspaceState
		for i := range st.Workspaces {
			if st.Workspaces[i].ID == w.ID {
				ws = &st.Workspaces[i]
			}
		}
		if ws == nil || ws.Cover != 1 {
			t.Fatalf("workspace %+v", ws)
		}
		i := w.ColumnOf(2)
		col := w.Engine().ColumnRects(w.Snapshot(), nil, layout.RectOpts{})[i]
		want := w.Output
		want.X = col.X
		if got := ws.Columns[i].Windows[0].Rect; got != want {
			t.Fatalf("held rect %+v, want %+v", got, want)
		}
		// The group swaps: same members, new cover.
		w.Fullscreen, w.Held = 2, []WindowID{1}
		if sameState(st, c.state()) {
			t.Fatal("cover change not republished")
		}
	})
}
