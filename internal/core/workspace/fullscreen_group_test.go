package workspace

import (
	"slices"
	"testing"
	"testing/synctest"
)

// checkGroup asserts the fullscreen group invariants.
func checkGroup(t *testing.T, w *Workspace, step string) {
	t.Helper()
	if w.Fullscreen != 0 && slices.Contains(w.Held, w.Fullscreen) {
		t.Errorf("%s: active cover %d is held %v", step, w.Fullscreen, w.Held)
	}
	for i, id := range w.Held {
		if !w.Has(id) {
			t.Errorf("%s: held %d is not on the workspace", step, id)
		}
		if slices.Contains(w.Held[i+1:], id) {
			t.Errorf("%s: held %d twice in %v", step, id, w.Held)
		}
	}
}

func groupWorkspace() *Workspace {
	w := cols(OverflowScroll, 3, 0, []WindowID{1}, []WindowID{2}, []WindowID{3}, []WindowID{4, 5})
	w.Fullscreen = 1
	w.hold(2)
	w.hold(3)
	w.hold(5)
	return w
}

func TestFullscreenGroupHold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := groupWorkspace()
		w.hold(1)  // active cover
		w.hold(2)  // duplicate
		w.hold(0)  // zero
		w.hold(99) // not on the workspace
		if want := []WindowID{2, 3, 5}; !slices.Equal(w.Held, want) {
			t.Fatalf("Held %v, want %v", w.Held, want)
		}
		for _, id := range []WindowID{1, 2, 3, 5} {
			if !w.IsFullscreenMember(id) {
				t.Errorf("%d not a member", id)
			}
		}
		for _, id := range []WindowID{0, 4, 99} {
			if w.IsFullscreenMember(id) {
				t.Errorf("%d is a member", id)
			}
		}
		checkGroup(t, w, "hold")
	})
}

func TestFullscreenGroupPromote(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := groupWorkspace()
		w.promoteHeld(3)
		if w.Fullscreen != 3 || !slices.Equal(w.Held, []WindowID{2, 1, 5}) || w.left != 0 {
			t.Fatalf("after promote: cover %d held %v left %d", w.Fullscreen, w.Held, w.left)
		}
		checkGroup(t, w, "promote")
		w.promoteHeld(4) // not held: no-op
		if w.Fullscreen != 3 {
			t.Fatalf("non-held promote changed cover to %d", w.Fullscreen)
		}
		// Without an active cover, the promoted window leaves Held.
		w.Fullscreen = 0
		w.promoteHeld(5)
		if w.Fullscreen != 5 || !slices.Equal(w.Held, []WindowID{2, 1}) || w.left != 0 {
			t.Fatalf("promote without cover: cover %d held %v left %d", w.Fullscreen, w.Held, w.left)
		}
		checkGroup(t, w, "promote empty")
		w.dropHeld(2)
		w.dropHeld(42)
		if !slices.Equal(w.Held, []WindowID{1}) {
			t.Fatalf("after drop: %v", w.Held)
		}
	})
}

// Every path that takes a window off the workspace drops it from Held.
func TestFullscreenGroupCleanup(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(w, to *Workspace)
		gone []WindowID
	}{
		{"remove", func(w, _ *Workspace) { w.RemoveWindow(2) }, []WindowID{2}},
		{"take column", func(w, _ *Workspace) { w.FocusID(5); w.takeColumn() }, []WindowID{4, 5}},
		{"take window", func(w, _ *Workspace) { w.FocusID(5); w.takeWindow() }, []WindowID{5}},
		{"move column", func(w, to *Workspace) { w.FocusID(3); MoveColumnTo(w, to, 0) }, []WindowID{3}},
		{"move window", func(w, to *Workspace) { w.FocusID(5); MoveWindowTo(w, to, 0) }, []WindowID{5}},
		{"move focused", func(w, to *Workspace) { w.FocusID(2); MoveFocused(w, to, true) }, []WindowID{2}},
		{"stash", func(w, _ *Workspace) { w.FocusID(3); w.ToggleWindowStash() }, []WindowID{3}},
		{"unfullscreen", func(w, _ *Workspace) { w.SetFullscreen(2, false) }, []WindowID{2}},
		{"refullscreen", func(w, _ *Workspace) { w.SetFullscreen(2, true) }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := groupWorkspace()
				to := cols(OverflowScroll, 3, 0, []WindowID{9})
				tc.run(w, to)
				for _, id := range tc.gone {
					if slices.Contains(w.Held, id) {
						t.Errorf("%d still held: %v", id, w.Held)
					}
				}
				checkGroup(t, w, tc.name)
				if len(to.Held) != 0 {
					t.Errorf("destination inherited Held %v", to.Held)
				}
			})
		})
	}
}

func placementOf(t *testing.T, ps []Placement, id WindowID) Placement {
	t.Helper()
	for _, p := range ps {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no placement for %d", id)
	return Placement{}
}

// A held tile is laid out fullscreen at its own column or band: output
// sized, off screen once the view settles on the cover, and it keeps that
// rect while the cover hides it.
func TestHeldTilePlacement(t *testing.T) {
	for _, overflow := range []Overflow{OverflowScroll, OverflowCascade} {
		t.Run(string(overflow), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := cols(overflow, 3, 0, []WindowID{1}, []WindowID{2}, []WindowID{3})
				w.SetUsable(Rect{Y: 20, W: 300, H: 80})
				w.Settle(false)
				// P (2) went fullscreen, then G (1) took over as cover.
				w.FocusID(2)
				w.SetFullscreen(2, true)
				w.Fullscreen = 0
				w.hold(2)
				w.FocusID(1)
				w.SetFullscreen(1, true)
				if w.Fullscreen != 1 || !slices.Equal(w.Held, []WindowID{2}) {
					t.Fatalf("cover %d held %v", w.Fullscreen, w.Held)
				}
				w.Settle(false)
				out := w.Output
				for _, covered := range []bool{true, false} {
					if !covered {
						w.Fullscreen = 0 // held only: nothing covers
					}
					col := w.columnRectsInto(nil, false)[w.ColumnOf(2)]
					want := out
					if overflow == OverflowScroll {
						want.X = col.X
					} else {
						want.Y += (col.Y - w.Usable.Y - w.Gap()) * out.H / w.Usable.H
					}
					p := placementOf(t, w.Layout(), 2)
					if !p.Fullscreen || p.Rect != want {
						t.Errorf("covered=%v: held %+v, want fullscreen at %+v", covered, p, want)
					}
					if covered && p.Rect.Overlaps(out) {
						t.Errorf("held %+v on screen under the cover", p.Rect)
					}
					if p.Hidden != covered {
						t.Errorf("covered=%v: hidden %v", covered, p.Hidden)
					}
				}
			})
		})
	}
}

// A held float is never shown.
func TestHeldFloatHidden(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := cols(OverflowScroll, 3, 0, []WindowID{1})
		w.AddFloating(7, 100, 50)
		w.hold(7)
		if p := placementOf(t, w.Layout(), 7); !p.Hidden || p.Fullscreen {
			t.Fatalf("held float %+v", p)
		}
	})
}

// Fixed overflow pins one cover over everything: it has no group.
func TestFullscreenGroupFixed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := cols(OverflowFixed, 3, 0, []WindowID{1}, []WindowID{2})
		w.Fullscreen = 1
		w.hold(2)
		if len(w.Held) != 0 {
			t.Fatalf("fixed held %v", w.Held)
		}
		w.SetOverflow(OverflowScroll)
		w.hold(2)
		if !slices.Equal(w.Held, []WindowID{2}) {
			t.Fatalf("scroll held %v", w.Held)
		}
		w.SetOverflow(OverflowCascade)
		if !slices.Equal(w.Held, []WindowID{2}) {
			t.Fatalf("cascade dropped held %v", w.Held)
		}
		w.SetOverflow(OverflowFixed)
		if len(w.Held) != 0 || w.Fullscreen != 1 {
			t.Fatalf("switch to fixed: cover %d held %v", w.Fullscreen, w.Held)
		}
	})
}
