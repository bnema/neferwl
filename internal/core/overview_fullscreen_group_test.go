package core

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/ports"
)

// groupOverview opens the overview over a cascade group (launcher 1 held,
// game 2 covering) with an unrelated window 3 on the workspace.
func groupOverview(t *testing.T) (*Core, *Monitor, *Workspace) {
	t.Helper()
	c, _, _ := fadeCore(t)
	w := c.cur().mon.Current()
	w.SetOverflow(OverflowCascade)
	c.clientEvent(context.Background(), ports.WindowMapped{ID: 3, PID: 30, AppID: "other", Proc: lineageNodes(30)})
	c.clientEvent(context.Background(), ports.WindowMapped{ID: 1, PID: 10, AppID: "launcher", Proc: lineageNodes(10)})
	c.clientEvent(context.Background(), ports.WindowFullscreenRequest{ID: 1, Fullscreen: true})
	c.clientEvent(context.Background(), ports.WindowMapped{ID: 2, PID: 20, AppID: "game", Proc: lineageNodes(20, 10)})
	if w.Fullscreen != 2 || !slices.Equal(w.Held, []WindowID{1}) {
		t.Fatalf("setup: cover %d held %v", w.Fullscreen, w.Held)
	}
	m := c.cur().mon
	m.ToggleOverview()
	return c, m, w
}

// selectColumn selects the column of id in the open overview, as its
// navigation binds do.
func selectColumn(m *Monitor, w *Workspace, id WindowID) {
	m.selectLaneColumn(w, w.ColumnOf(id))
}

// Closing the overview on a group member swaps the cover; on an unrelated
// window it demotes the group, with no leave latch.
func TestOverviewSelectInFullscreenGroup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target WindowID
		cover  WindowID
		held   []WindowID
	}{
		{"held member", 1, 1, []WindowID{2}},
		{"unrelated", 3, 0, []WindowID{1, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, m, w := groupOverview(t)
				selectColumn(m, w, tc.target)
				if got := m.overviewTarget(); got != tc.target {
					t.Fatalf("overview target %d", got)
				}
				m.ToggleOverview()
				held := slices.Clone(w.Held)
				slices.Sort(held)
				if w.Fullscreen != tc.cover || !slices.Equal(held, tc.held) || w.Inspect().Left != 0 {
					t.Fatalf("cover %d held %v left %d", w.Fullscreen, w.Held, w.Inspect().Left)
				}
				if f, _ := w.Focused(); f != tc.target {
					t.Fatalf("focus %d want %d", f, tc.target)
				}
			})
		})
	}
}

// Moving a selection to another workspace from the overview keeps the
// source group consistent and never sets the leave latch: a held member
// leaves the group; for an unrelated window the cover is demoted, then the
// focus falls back on a member, which covers again (focus promotes).
func TestOverviewMoveInFullscreenGroup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target WindowID
		cover  WindowID
		held   []WindowID
	}{
		{"held member", 1, 2, nil},
		{"unrelated", 3, 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, m, w := groupOverview(t)
				selectColumn(m, w, tc.target)
				m.overviewMoveTo(Action("move-column-to-workspace 2"))
				if w.Has(tc.target) {
					t.Fatalf("%d not moved", tc.target)
				}
				if w.Inspect().Left != 0 {
					t.Fatalf("leave latch %d", w.Inspect().Left)
				}
				if tc.cover != 0 {
					if w.Fullscreen != tc.cover || len(w.Held) != 0 {
						t.Fatalf("source cover %d held %v", w.Fullscreen, w.Held)
					}
					return
				}
				// Both members stay in the group: the focused one covers.
				members := append([]WindowID{w.Fullscreen}, w.Held...)
				slices.Sort(members)
				if f, _ := w.Focused(); !slices.Equal(members, []WindowID{1, 2}) || f != w.Fullscreen {
					t.Fatalf("source cover %d held %v focus %d", w.Fullscreen, w.Held, f)
				}
			})
		})
	}
}
