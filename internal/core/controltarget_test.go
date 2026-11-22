package core_test

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// doTarget runs a control action on the window holding mark.
func (r chainRig) doTarget(t *testing.T, mark, action string) ports.ControlReply {
	t.Helper()
	reply := make(chan ports.ControlReply, 1)
	r.control <- ports.ControlDo{Caps: domain.CapAct, Wait: domain.WaitCommitted, Action: action, Target: mark, Reply: reply}
	synctest.Wait()
	select {
	case rep := <-reply:
		return rep
	default:
		t.Fatalf("do --target %s %q: no reply", mark, action)
		return ports.ControlReply{}
	}
}

// mustTarget runs a targeted action that must succeed.
func (r chainRig) mustTarget(t *testing.T, mark, action string) {
	t.Helper()
	if rep := r.doTarget(t, mark, action); rep.Err != "" {
		t.Fatalf("--target %s %q: %+v", mark, action, rep)
	}
}

// where is the focused window and the active workspace of the output.
func where(st domain.State) (domain.WindowID, int) {
	return stateFocus(st), st.Outputs[0].Active
}

func TestTargetOtherWorkspaceKeepsFocus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "none")
		r.mustDo(t, "mark a; move-window-to-workspace 2")
		before := r.current(t)
		r.mustTarget(t, "a", "toggle-floating")
		st := r.current(t)
		if !stateWindow(t, st, 5).Floating {
			t.Fatal("target not floating")
		}
		if f, ws := where(st); f != stateFocus(before) || ws != before.Outputs[0].Active {
			t.Fatalf("focus %d ws %d moved", f, ws)
		}
	})
}

func TestTargetSameWorkspaceRestoresFocus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "none")
		r.mustDo(t, "focus-column-left; focus-column-left; mark a; focus-column-right; focus-column-right")
		r.mustTarget(t, "a", "toggle-floating")
		st := r.current(t)
		if !stateWindow(t, st, 3).Floating || stateFocus(st) != 5 {
			t.Fatalf("3 floating %v, focus %d", stateWindow(t, st, 3).Floating, stateFocus(st))
		}
	})
}

func TestTargetCoverEndsFullscreen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "none")
		r.mustDo(t, "mark a; toggle-fullscreen")
		if !stateWindow(t, r.current(t), 5).Fullscreen {
			t.Fatal("setup")
		}
		r.mustTarget(t, "a", "toggle-fullscreen")
		if stateWindow(t, r.current(t), 5).Fullscreen {
			t.Fatal("still fullscreen")
		}
	})
}

// A float under another window's fullscreen cannot take the workspace's
// focus: it is left alone.
func TestTargetUnreachableIsNoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "none")
		// Another window covers the workspace fullscreen: a float under it
		// cannot take the focus. (A tile can: focusing it scrolls the
		// cover away, as a key would.)
		r.mustDo(t, "mark a; toggle-floating; focus-column-left; toggle-fullscreen")
		cover := stateFocus(r.current(t))
		if st := r.current(t); !stateWindow(t, st, 5).Floating || cover == 5 || !stateWindow(t, st, cover).Fullscreen {
			t.Fatalf("setup: focus %d %+v", stateFocus(st), st.Windows)
		}
		r.mustTarget(t, "a", "toggle-floating")
		st := r.current(t)
		if !stateWindow(t, st, 5).Floating || !stateWindow(t, st, cover).Fullscreen || stateFocus(st) != cover {
			t.Fatalf("covered target changed: %+v", st.Windows)
		}
	})
}

func TestTargetHiddenStashIsNoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "none")
		r.mustDo(t, "mark b; toggle-window-stash; toggle-stash-visible")
		if w := stateWindow(t, r.current(t), 5); w.StashIndex == 0 || !w.Hidden {
			t.Fatalf("setup %+v", w)
		}
		r.mustTarget(t, "b", "toggle-window-stash")
		if w := stateWindow(t, r.current(t), 5); w.StashIndex == 0 || !w.Hidden {
			t.Fatalf("hidden stash target changed: %+v", w)
		}
	})
}

func TestTargetCloseWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "none")
		r.mustDo(t, "focus-column-left; mark a; focus-column-right")
		r.drain()
		r.mustTarget(t, "a", "close-window")
		closed := false
		for _, cmd := range r.sent() {
			if c, ok := cmd.(ports.CloseWindow); ok {
				if c.ID != 4 {
					t.Fatalf("closed %d", c.ID)
				}
				closed = true
			}
		}
		if !closed {
			t.Fatal("no CloseWindow")
		}
	})
}

func TestTargetMoveWindowStays(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "none")
		r.mustDo(t, "focus-column-left; mark a; focus-column-right")
		r.mustTarget(t, "a", "move-window-to-workspace 2")
		st := r.current(t)
		if w := stateWindow(t, st, 4); w.Workspace != 2 {
			t.Fatalf("target on %d", w.Workspace)
		}
		if f, ws := where(st); f != 5 || ws != 1 {
			t.Fatalf("focus %d ws %d", f, ws)
		}
	})
}

func TestTargetErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "none")
		r.mustDo(t, "mark a")
		for _, action := range []string{"focus-column-left", "close-window; close-window", "mark b"} {
			if rep := r.doTarget(t, "a", action); rep.Err != ports.ControlErrBadRequest {
				t.Fatalf("%q: %+v, want bad-request", action, rep)
			}
		}
		if rep := r.doTarget(t, "zz", "close-window"); rep.Err != ports.ControlErrNoTarget {
			t.Fatalf("missing: %+v", rep)
		}
		r.epoch.Store(domain.SecurityState{Generation: 1, Protected: true})
		if rep := r.doTarget(t, "a", "close-window"); rep.Err != ports.ControlErrLocked {
			t.Fatalf("locked: %+v", rep)
		}
		if rep := r.doTarget(t, "zz", "close-window"); rep.Err != ports.ControlErrLocked {
			t.Fatalf("locked, missing mark: %+v", rep)
		}
	})
}

// A targeted move to the workspace on screen keeps its keyboard focus; a
// cover there refuses the move.
func TestTargetMoveToShownWorkspace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "none")
		r.mustDo(t, "mark a; move-window-to-workspace 2")
		focus := stateFocus(r.current(t))
		r.mustTarget(t, "a", "move-window-to-workspace 1")
		st := r.current(t)
		if stateWindow(t, st, 5).Workspace != 1 || stateFocus(st) != focus {
			t.Fatalf("5 on %d, focus %d want %d", stateWindow(t, st, 5).Workspace, stateFocus(st), focus)
		}
		r.mustDo(t, "focus-column-left; move-window-to-workspace 2; toggle-fullscreen")
		cover := stateFocus(r.current(t))
		r.mustDo(t, "focus-workspace 2; mark b; focus-workspace 1")
		r.mustTarget(t, "b", "move-window-to-workspace 1")
		st = r.current(t)
		held := stateWindow(t, st, cover)
		if !held.Fullscreen || stateFocus(st) != cover {
			t.Fatalf("cover changed: %+v", held)
		}
		for _, w := range st.Windows {
			if w.Marks == "b" && w.Workspace != 2 {
				t.Fatalf("moved behind a cover: %+v", w)
			}
		}
	})
}

// The borrowed selection keeps a maximized column of a fixed layout.
func TestTargetKeepsMaximizedColumn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRigWith(t, "none", func(c *domain.Config) { c.Layout.Overflow = "fixed" })
		r.mustDo(t, "focus-column-left; mark a; focus-column-right; maximize-column")
		widths := func() []int {
			var w []int
			for _, col := range r.current(t).Workspaces[0].Columns {
				w = append(w, col.Rect.W)
			}
			return w
		}
		before := widths()
		r.mustTarget(t, "a", "toggle-floating")
		st := r.current(t)
		if !stateWindow(t, st, 4).Floating || stateFocus(st) != 5 {
			t.Fatalf("4 floating %v, focus %d", stateWindow(t, st, 4).Floating, stateFocus(st))
		}
		after := widths()
		if len(after) == 0 || after[len(after)-1] != before[len(before)-1] || after[len(after)-1] < 400 {
			t.Fatalf("maximized column lost: %v -> %v", before, after)
		}
	})
}

// close-window closes a target in a hidden stash.
func TestTargetCloseHiddenStash(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "none")
		r.mustDo(t, "mark a; toggle-window-stash; toggle-stash-visible")
		r.drain()
		r.mustTarget(t, "a", "close-window")
		for _, cmd := range r.sent() {
			if c, ok := cmd.(ports.CloseWindow); ok && c.ID == 5 {
				return
			}
		}
		t.Fatal("no CloseWindow for the stashed target")
	})
}

// A stash shown over a cover stays shown through a targeted action.
func TestTargetKeepsStashOverCover(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "none")
		r.mustDo(t, "focus-column-left; focus-column-left; mark a; focus-column-right; focus-column-right; toggle-window-stash; focus-column-left; toggle-fullscreen; toggle-stash-visible")
		st := r.current(t)
		if w := stateWindow(t, st, 5); w.StashIndex == 0 || w.Hidden || !w.Visible {
			t.Fatalf("setup: stash %+v focus %d", w, stateFocus(st))
		}
		r.mustTarget(t, "a", "toggle-floating")
		if w := stateWindow(t, r.current(t), 5); w.Hidden || !w.Visible {
			t.Fatalf("stash over cover lost: %+v", w)
		}
	})
}
