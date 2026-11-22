package core_test

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// stateWindow returns the state of id in st.
func stateWindow(t *testing.T, st domain.State, id domain.WindowID) domain.WindowState {
	t.Helper()
	for _, w := range st.Windows {
		if w.ID == id {
			return w
		}
	}
	t.Fatalf("window %d not in state", id)
	return domain.WindowState{}
}

func stateFocus(st domain.State) domain.WindowID {
	if st.Window == nil {
		return 0
	}
	return st.Window.ID
}

// mustDo runs a control action that must succeed.
func (r chainRig) mustDo(t *testing.T, action string) {
	t.Helper()
	if got := r.do(t, action); got.Err != "" {
		t.Fatalf("%q: %+v", action, got)
	}
}

// A mark bind marks the focused window; marking another window moves the
// name, and the state shows it.
func TestMarkBindMovesName(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "mark a")
		r.send(cmdX)
		synctest.Wait()
		if got := stateWindow(t, r.current(t), 5).Marks; got != "a" {
			t.Fatalf("marks of 5 %q", got)
		}
		r.mustDo(t, "focus-column-left; mark a")
		st := r.current(t)
		if stateWindow(t, st, 5).Marks != "" || stateWindow(t, st, 4).Marks != "a" {
			t.Fatalf("mark did not move: %+v", st.Windows)
		}
		r.mustDo(t, "toggle-mark b")
		if got := stateWindow(t, r.current(t), 4).Marks; got != "a b" {
			t.Fatalf("toggle: %q", got)
		}
		r.mustDo(t, "unmark")
		if got := stateWindow(t, r.current(t), 4).Marks; got != "" {
			t.Fatalf("unmark: %q", got)
		}
	})
}

// focus-mark switches to the holder's workspace and focuses it.
func TestFocusMarkAcrossWorkspaces(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "none")
		r.mustDo(t, "mark a; move-window-to-workspace 2")
		st := r.current(t)
		if w := stateWindow(t, st, 5); w.Workspace != 2 || w.Visible {
			t.Fatalf("setup %+v", w)
		}
		r.mustDo(t, "focus-mark a")
		st = r.current(t)
		if w := stateWindow(t, st, 5); !w.Visible || stateFocus(st) != 5 {
			t.Fatalf("focus-mark: %+v focused %d", w, stateFocus(st))
		}
	})
}

// focus-mark shows a window from a hidden stash.
func TestFocusMarkHiddenStash(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "none")
		r.mustDo(t, "mark a; toggle-window-stash")
		r.mustDo(t, "toggle-stash-visible")
		if w := stateWindow(t, r.current(t), 5); w.StashIndex == 0 || !w.Hidden {
			t.Fatalf("setup %+v", w)
		}
		r.mustDo(t, "focus-mark a")
		st := r.current(t)
		if w := stateWindow(t, st, 5); w.Hidden || stateFocus(st) != 5 {
			t.Fatalf("focus-mark: %+v focused %d", w, stateFocus(st))
		}
	})
}

// A missing mark is a no-op that keeps the chain going, also once its
// holder unmapped.
func TestFocusMarkMissing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "none")
		r.mustDo(t, "focus-mark zz; focus-column-left")
		if got := stateFocus(r.current(t)); got != 4 {
			t.Fatalf("focused %d, want 4", got)
		}
		r.mustDo(t, "mark a; focus-column-left")
		r.client <- ports.WindowUnmapped{ID: 4}
		synctest.Wait()
		before := stateFocus(r.current(t))
		r.mustDo(t, "focus-mark a")
		if got := stateFocus(r.current(t)); got != before {
			t.Fatalf("focused %d, want %d", got, before)
		}
	})
}

// Mark actions do nothing while the focused screen's overview is open.
func TestMarkOverviewNoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "none")
		r.mustDo(t, "toggle-overview")
		r.mustDo(t, "mark a")
		for _, w := range r.current(t).Windows {
			if w.Marks != "" {
				t.Fatalf("window %d marked in overview: %q", w.ID, w.Marks)
			}
		}
		r.mustDo(t, "toggle-overview")
		r.mustDo(t, "mark a")
		if got := stateWindow(t, r.current(t), 5).Marks; got != "a" {
			t.Fatalf("after overview: %q", got)
		}
	})
}

// markMoveRig runs two outputs with mark binds on Alt+Ctrl+Shift: m marks
// a, w swaps with a, v moves below a, o and p focus the right and left
// outputs, d toggles the named workspace dev, whose slot runs code.
func markMoveRig(t *testing.T) *multiRig {
	t.Helper()
	return startMulti(t, func(c *domain.Config) {
		for k, a := range map[string]string{"m": "mark a", "w": "swap-mark a", "v": "move-to-mark a", "o": "focus-monitor-right", "p": "focus-monitor-left", "d": "workspace dev"} {
			c.Binds["Alt+Ctrl+Shift+"+k] = a
		}
		c.Workspaces = []domain.WorkspaceConfig{{Name: "dev", Slots: []domain.SlotConfig{{Index: 1, Cells: 1, Argv: []string{"code"}}}}}
	}, left, right)
}

// press sends a mark rig bind and returns the state it published.
func (r *multiRig) press(t *testing.T, key string) domain.State {
	t.Helper()
	r.input <- ports.KeyEvent{Keysym: key, Mods: domain.ModAlt | domain.ModCtrl | domain.ModShift, Pressed: true}
	return stateAfter(t, r.state, func(domain.State) bool { return true })
}

// swap-mark across outputs exchanges the windows; the focus stays in place,
// on the marked window.
func TestSwapMarkAcrossOutputs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := markMoveRig(t)
		r.mapApp(t, 1, "")
		r.press(t, "o")
		r.mapApp(t, 2, "")
		r.mapApp(t, 3, "")
		r.press(t, "m")
		r.press(t, "p")
		st := r.press(t, "w")
		if a, b := stWindow(st, 3), stWindow(st, 1); a.Output != "DP-1" || b.Output != "DP-2" || stFocus(st) != 3 {
			t.Fatalf("3 on %s, 1 on %s, focus %d", a.Output, b.Output, stFocus(st))
		}
	})
}

// move-to-mark puts the focused tile below the marked one on another
// workspace, focused.
func TestMoveToMarkAcrossWorkspaces(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := markMoveRig(t)
		r.mapApp(t, 1, "")
		r.press(t, "m")
		r.key(t, "Next", domain.ModAlt)
		r.mapApp(t, 2, "")
		r.mapApp(t, 3, "")
		st := r.press(t, "v")
		target, moved := stWindow(st, 1), stWindow(st, 3)
		if moved.Workspace != target.Workspace || moved.Column != target.Column || moved.Row != target.Row+1 || stFocus(st) != 3 {
			t.Fatalf("moved %+v target %+v focus %d", moved, target, stFocus(st))
		}
	})
}

// A slot window does not trade places.
func TestSwapMarkRefusesSlotWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := markMoveRig(t)
		r.mapApp(t, 1, "")
		r.press(t, "d")
		code := receive(t, r.spawn)
		r.client <- ports.WindowMapped{ID: 2, Slot: slotToken(t, code)}
		r.stateWith(t, 2)
		r.press(t, "m")
		r.press(t, "d")
		r.input <- ports.KeyEvent{Keysym: "w", Mods: domain.ModAlt | domain.ModCtrl | domain.ModShift, Pressed: true}
		synctest.Wait()
		if len(r.state) != 0 {
			t.Fatal("refused swap published a state")
		}
		// Moving the mark to 1 publishes the layout.
		st := r.press(t, "m")
		if w := stWindow(st, 2); w.Marks != "" || w.WorkspaceName != "dev" || stWindow(st, 1).WorkspaceName == "dev" || stWindow(st, 1).Marks != "a" {
			t.Fatalf("slot window moved: %+v", st.Windows)
		}
	})
}
