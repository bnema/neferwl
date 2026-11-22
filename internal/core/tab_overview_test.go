package core_test

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// A hidden tab keeps its configure across an overview open and close.
func TestTabbedOverviewKeepsHiddenTabConfigure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startMulti(t, func(c *domain.Config) { c.Binds["F2"] = "toggle-overview" }, left)
		r.mapWindow(t, 1)
		r.mapWindow(t, 2)
		r.key(t, "bracketleft", domain.ModAlt)
		r.key(t, "t", domain.ModAlt)
		synctest.Wait()
		for len(r.commands) > 0 {
			<-r.commands
		}
		r.key(t, "F2", 0)
		synctest.Wait()
		r.key(t, "F2", 0)
		synctest.Wait()
		for len(r.commands) > 0 {
			if v, ok := (<-r.commands).(ports.ConfigureWindow); ok && v.Width == 0 && v.Height == 0 {
				t.Fatalf("zero-size configure %+v", v)
			}
		}
	})
}

// tab-window-right moves the focused window into the right column as its
// visible tab.
func TestTabWindowRight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startMulti(t, func(c *domain.Config) { c.Binds["F3"] = "tab-window-right" }, left)
		r.mapWindow(t, 1)
		r.mapWindow(t, 2)
		r.key(t, "h", domain.ModAlt)
		r.key(t, "F3", 0)
		st := stateAfter(t, r.state, func(st domain.State) bool {
			for _, ws := range st.Workspaces {
				if len(ws.Columns) == 1 && ws.Columns[0].Tabbed {
					return true
				}
			}
			return false
		})
		for _, ws := range st.Workspaces {
			if len(ws.Columns) != 1 {
				continue
			}
			c := ws.Columns[0]
			if len(c.Windows) != 2 || c.Windows[0].ID != 2 || c.Windows[1].ID != 1 || c.Active != 2 || stFocus(st) != 1 {
				t.Fatalf("column %+v focus %d", c, stFocus(st))
			}
		}
	})
}
