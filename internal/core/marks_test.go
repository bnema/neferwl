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
