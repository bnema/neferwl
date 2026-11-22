package core_test

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// cmd+t tabs the column: the hidden tab is configured not visible, the
// visible one is, below the tab bar.
func TestTabbedHiddenTabsNotVisible(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startMulti(t, nil, left)
		r.mapWindow(t, 1)
		r.mapWindow(t, 2)
		r.key(t, "bracketleft", domain.ModAlt) // stack 2 under 1
		for len(r.commands) > 0 {
			<-r.commands
		}
		r.key(t, "t", domain.ModAlt)
		synctest.Wait()
		last := map[domain.WindowID]ports.ConfigureWindow{}
		for len(r.commands) > 0 {
			if v, ok := (<-r.commands).(ports.ConfigureWindow); ok {
				last[v.ID] = v
			}
		}
		hid, ok := last[1]
		if !ok || hid.Visible {
			t.Fatalf("hidden tab configure %+v (sent %v)", hid, ok)
		}
		vis, ok := last[2]
		if !ok || !vis.Visible || vis.Height >= 100 {
			t.Fatalf("visible tab configure %+v (sent %v)", vis, ok)
		}
	})
}

// latestScene is the newest scene set on the channel, if any.
func latestScene(t *testing.T, ch chan []domain.Scene) ([]domain.Scene, bool) {
	t.Helper()
	synctest.Wait()
	select {
	case s := <-ch:
		return s, true
	default:
		return nil, false
	}
}

// tabbedRig maps three windows into one column, tabbed, the last one
// visible.
func tabbedRig(t *testing.T) (*multiRig, []domain.Scene) {
	t.Helper()
	r := startMulti(t, nil, left)
	r.mapWindow(t, 1)
	r.mapWindow(t, 2)
	r.key(t, "bracketleft", domain.ModAlt)
	r.mapWindow(t, 3)
	r.key(t, "bracketleft", domain.ModAlt)
	for _, id := range []domain.WindowID{1, 2, 3} {
		r.client <- ports.WindowTitle{ID: id, Title: string(rune('a' + id - 1))}
	}
	latestScene(t, r.scenes)
	set := r.key(t, "t", domain.ModAlt)
	if s, ok := latestScene(t, r.scenes); ok {
		set = s
	}
	return r, set
}

func TestTabBarScene(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, before := tabbedRig(t)
		r.client <- ports.WindowTitle{ID: 2, Title: "b"} // unchanged: same scene
		if after, ok := latestScene(t, r.scenes); ok && (before == nil || after[0].Seq != before[0].Seq || &after[0].TabBars[0] != &before[0].TabBars[0]) {
			t.Fatalf("unchanged title gave a new scene or new tab bars: before %v after %+v", before != nil, after[0].TabBars)
		}
		r.key(t, "k", domain.ModAlt) // focus-window-up: tab 2
		r.client <- ports.WindowTitle{ID: 1, Title: "renamed"}
		set, ok := latestScene(t, r.scenes)
		if !ok || len(set[0].TabBars) != 1 {
			t.Fatalf("scene %v %+v", ok, set)
		}
		bar := set[0].TabBars[0]
		if len(bar.Tabs) != 3 || bar.Rect.W != 200 || bar.Rect.Y != 0 || bar.Rect.H <= 0 {
			t.Fatalf("bar %+v", bar)
		}
		wantTitles := []string{"renamed", "b", "c"}
		x := 0
		for j, tab := range bar.Tabs {
			if tab.Window != domain.WindowID(j+1) || tab.Title != wantTitles[j] || tab.Active != (j == 1) || tab.Rect.X != x || tab.Rect.Y != bar.Rect.Y || tab.Rect.H != bar.Rect.H {
				t.Fatalf("tab %d %+v", j, tab)
			}
			x += tab.Rect.W
		}
		if bar.Tabs[0].Rect.W != 66 || bar.Tabs[2].Rect.W != 68 {
			t.Fatalf("tab widths %+v", bar.Tabs)
		}
		for _, w := range set[0].Windows {
			if w.ID == 2 && w.Rect.Y != bar.Rect.H {
				t.Fatalf("visible tab %+v under bar %+v", w, bar)
			}
		}
	})
}

func TestTabBarHiddenWhenFullscreenOrOffScreen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _ := tabbedRig(t)
		set := r.key(t, "f", domain.ModAlt|domain.ModShift)
		if s, ok := latestScene(t, r.scenes); ok {
			set = s
		}
		if len(set[0].TabBars) != 0 {
			t.Fatalf("fullscreen tab has a bar: %+v", set[0].TabBars)
		}
		r.key(t, "f", domain.ModAlt|domain.ModShift)
		latestScene(t, r.scenes)
		// Two new columns scroll the tabbed one off the output.
		r.mapWindow(t, 4)
		set = r.mapWindow(t, 5)
		if s, ok := latestScene(t, r.scenes); ok {
			set = s
		}
		if len(set[0].TabBars) != 0 {
			t.Fatalf("off-screen column has a bar: %+v", set[0].TabBars)
		}
	})
}

func TestTabbedStateActive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _ := tabbedRig(t)
		col := func() domain.ColumnState {
			synctest.Wait()
			var st domain.State
			for len(r.state) > 0 {
				st = <-r.state
			}
			for _, w := range st.Workspaces {
				if w.Visible && len(w.Columns) > 0 {
					return w.Columns[0]
				}
			}
			t.Fatalf("no column in %+v", st)
			return domain.ColumnState{}
		}
		if c := col(); !c.Tabbed || c.Active != 3 {
			t.Fatalf("tabbed column %+v", c)
		}
		r.key(t, "k", domain.ModAlt)
		if c := col(); !c.Tabbed || c.Active != 2 {
			t.Fatalf("after focus up %+v", c)
		}
		r.key(t, "t", domain.ModAlt)
		if c := col(); c.Tabbed || c.Active != 0 {
			t.Fatalf("untabbed %+v", c)
		}
	})
}

// Every tab of a tabbed column reports the content rect below the bar.
func TestTabbedStateTileRects(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, set := tabbedRig(t)
		bar := set[0].TabBars[0].Rect
		synctest.Wait()
		var st domain.State
		for len(r.state) > 0 {
			st = <-r.state
		}
		for _, w := range st.Workspaces {
			if !w.Visible {
				continue
			}
			c := w.Columns[0]
			want := domain.Rect{X: c.Rect.X, Y: c.Rect.Y + bar.H, W: c.Rect.W, H: c.Rect.H - bar.H}
			for _, tile := range c.Windows {
				if tile.Rect != want {
					t.Fatalf("tile %+v, want %+v", tile, want)
				}
			}
			return
		}
		t.Fatal("no visible workspace")
	})
}

// With gaps the bar sits right above the visible tab, rounded like it.
func TestTabBarWithGaps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startMulti(t, func(c *domain.Config) { c.Layout.Gaps = 8; c.Border.Radius = 10 }, left)
		r.mapWindow(t, 1)
		r.mapWindow(t, 2)
		r.key(t, "bracketleft", domain.ModAlt)
		set := r.key(t, "t", domain.ModAlt)
		if s, ok := latestScene(t, r.scenes); ok {
			set = s
		}
		if len(set[0].TabBars) != 1 {
			t.Fatalf("bars %+v", set[0].TabBars)
		}
		bar := set[0].TabBars[0]
		for _, w := range set[0].Windows {
			if w.ID == 2 && (w.Rect.Y != bar.Rect.Y+bar.Rect.H || w.Inset&domain.SideTop != 0 || w.Rect.X != bar.Rect.X || w.Rect.W != bar.Rect.W) {
				t.Fatalf("tab %+v, bar %+v", w.Rect, bar.Rect)
			}
		}
		if bar.Radius != 10 || bar.Rect.Y != 8 {
			t.Fatalf("bar %+v", bar)
		}
	})
}
