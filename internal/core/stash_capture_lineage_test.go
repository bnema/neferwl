package core

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/ports"
)

// With stash.capture and the stash shown, a new window lands in the stash
// whatever placed it: a plain arrival, the child of a window in the stash,
// or the child of a tile on the workspace (lineage).
func TestStashCaptureLineage(t *testing.T) {
	for _, mode := range []Overflow{OverflowScroll, OverflowCascade} {
		for _, tc := range []struct {
			name  string
			child ports.WindowMapped
		}{
			{"unrelated", ports.WindowMapped{ID: 5, PID: 50, AppID: "app", Proc: lineageNodes(50)}},
			{"child of stashed terminal", ports.WindowMapped{ID: 5, PID: 50, AppID: "app", Proc: lineageNodes(50, 10)}},
			{"child of tile", ports.WindowMapped{ID: 5, PID: 50, AppID: "app", Proc: lineageNodes(50, 20)}},
		} {
			t.Run(string(mode)+"/"+tc.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					c, _, _ := fadeCore(t)
					m := c.cur().mon
					m.SetStashCapture(true)
					w := m.Current()
					w.SetOverflow(mode)
					c.clientEvent(context.Background(), ports.WindowMapped{ID: 1, PID: 10, AppID: "terminal", Proc: lineageNodes(10)})
					c.clientEvent(context.Background(), ports.WindowMapped{ID: 2, PID: 20, AppID: "launcher", Proc: lineageNodes(20)})
					w.FocusID(1)
					w.ToggleWindowStash()
					if w.StashIndex(1) < 0 || !w.StashShown() {
						t.Fatalf("setup: stash %+v shown %v", w.Stash, w.StashShown())
					}
					c.clientEvent(context.Background(), tc.child)
					if w.StashIndex(5) < 0 {
						t.Fatalf("arrival bypassed the stash (path %q): columns %+v stash %+v", c.placementPath, w.Columns, w.Stash)
					}
					if f, _ := w.Focused(); f != 5 {
						t.Fatalf("focus %d", f)
					}
				})
			})
		}
	}
}

// The launcher of a new window is on another workspace of the monitor whose
// shown stash captures: capture wins over lineage placement, as on main
// (an open stash is explicit user intent).
func TestStashCaptureWinsOverLineage(t *testing.T) {
	for _, mode := range []Overflow{OverflowScroll, OverflowCascade} {
		for _, capture := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/capture=%v", mode, capture), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					c, _, _ := fadeCore(t)
					m := c.cur().mon
					m.SetStashCapture(capture)
					c.clientEvent(context.Background(), ports.WindowMapped{ID: 1, PID: 10, AppID: "tile", Proc: lineageNodes(10)})
					c.clientEvent(context.Background(), ports.WindowMapped{ID: 2, PID: 20, AppID: "terminal", Proc: lineageNodes(20)})
					w := m.Current()
					w.SetOverflow(mode)
					// The terminal goes to workspace 2; the user stays on 1
					// with the tile stashed, the stash shown.
					w.FocusID(2)
					c.applyAction(ActionMoveWindowToWorkspaceNext)
					m.Focus(indexOf(m.Workspaces, w))
					w.FocusID(1)
					w.ToggleWindowStash()
					if other, _ := m.find(2); other == w || !w.StashShown() || m.Current() != w {
						t.Fatalf("setup: terminal on %v, stash shown %v", other == w, w.StashShown())
					}
					c.clientEvent(context.Background(), ports.WindowMapped{ID: 5, PID: 50, AppID: "app", Proc: lineageNodes(50, 20)})
					if c.launcherOf(5) != 2 {
						t.Fatalf("launcher %d", c.launcherOf(5))
					}
					if !capture {
						// Without capture, lineage still places it beside
						// its launcher, on workspace 2.
						if to, _ := m.find(5); to == w || to == nil || c.placementPath != "lineage" {
							t.Fatalf("no-capture placement on current %v path %q", to == w, c.placementPath)
						}
						return
					}
					if w.StashIndex(5) < 0 {
						t.Fatalf("lineage bypassed the stash (path %q): on %v", c.placementPath, w.Has(5))
					}
					if f, _ := m.Focused(); f != 5 || c.keyboardFocus() != 5 {
						t.Fatalf("focus %d keyboard %d", f, c.keyboardFocus())
					}
				})
			})
		}
	}
}

// Capture wins over fullscreen inheritance: in scroll, the launcher's
// cover is scrolled off (no window covers) and the stash shows; its child
// goes to the stash instead of taking the fullscreen.
func TestStashCaptureWinsOverInheritance(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _, _ := fadeCore(t)
		m := c.cur().mon
		m.SetStashCapture(true)
		w := m.Current()
		w.SetOverflow(OverflowScroll)
		c.clientEvent(context.Background(), ports.WindowMapped{ID: 1, PID: 10, AppID: "launcher", Proc: lineageNodes(10)})
		c.clientEvent(context.Background(), ports.WindowFullscreenRequest{ID: 1, Fullscreen: true})
		c.clientEvent(context.Background(), ports.WindowMapped{ID: 2, PID: 20, AppID: "tile", Proc: lineageNodes(20)})
		c.clientEvent(context.Background(), ports.WindowMapped{ID: 3, PID: 30, AppID: "terminal", Proc: lineageNodes(30)})
		w.FocusID(3)
		w.ToggleWindowStash()
		w.FocusID(2) // scrolls the cover off; the stash stays shown
		if !w.StashShown() {
			w.ToggleStashVisible()
		}
		if w.Fullscreen != 1 || w.Cover() != 0 || !w.StashShown() {
			t.Fatalf("setup: fullscreen %d cover %d shown %v", w.Fullscreen, w.Cover(), w.StashShown())
		}
		c.clientEvent(context.Background(), ports.WindowMapped{ID: 5, PID: 50, AppID: "game", Proc: lineageNodes(50, 10)})
		if w.StashIndex(5) < 0 || w.IsFullscreenMember(5) || w.Fullscreen != 1 || len(w.Held) != 0 {
			t.Fatalf("path %q stash %+v fullscreen %d held %v", c.placementPath, w.Stash, w.Fullscreen, w.Held)
		}
	})
}
