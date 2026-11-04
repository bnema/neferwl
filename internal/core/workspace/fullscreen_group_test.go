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
					// A tiled cover leaves the held tile shown, off screen.
					if p.Hidden {
						t.Errorf("covered=%v: held tile hidden", covered)
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

// Client requests against a group: P held, G the active cover.
func TestFullscreenGroupRequests(t *testing.T) {
	for _, overflow := range []Overflow{OverflowScroll, OverflowCascade} {
		t.Run(string(overflow), func(t *testing.T) {
			setup := func() *Workspace {
				w := cols(overflow, 3, 0, []WindowID{1}, []WindowID{2}, []WindowID{3})
				w.Settle(false)
				// P (2) fullscreen, then held; G (1) becomes the cover.
				w.FocusID(2)
				w.SetFullscreen(2, true)
				w.Fullscreen = 0
				w.hold(2)
				w.FocusID(1)
				w.SetFullscreen(1, true)
				if w.Fullscreen != 1 || !slices.Equal(w.Held, []WindowID{2}) {
					t.Fatalf("setup: cover %d held %v", w.Fullscreen, w.Held)
				}
				return w
			}
			synctest.Test(t, func(t *testing.T) {
				// P re-requests fullscreen, unfocused or focused.
				w := setup()
				w.SetFullscreen(2, true)
				if w.Fullscreen != 1 || !slices.Equal(w.Held, []WindowID{2}) {
					t.Fatalf("P re-request: cover %d held %v", w.Fullscreen, w.Held)
				}
				// Focus promotes P; its request then keeps it the cover.
				w.FocusID(2)
				w.SetFullscreen(2, true)
				if w.Fullscreen != 2 || !slices.Equal(w.Held, []WindowID{1}) {
					t.Fatalf("focused P re-request: cover %d held %v", w.Fullscreen, w.Held)
				}

				// P unsets: it leaves the group, its column shrinks back.
				w = setup()
				w.SetFullscreen(2, false)
				if w.Fullscreen != 1 || len(w.Held) != 0 {
					t.Fatalf("P unset: cover %d held %v", w.Fullscreen, w.Held)
				}
				if w.HeldColumn(w.ColumnOf(2)) || w.FullscreenColumn(w.ColumnOf(2)) {
					t.Fatal("P column still fullscreen-sized")
				}
				if overflow == OverflowScroll {
					v := w.Snapshot()
					if got := w.Engine().ColumnWidth(v, w.ColumnOf(2), false); got == w.Output.W {
						t.Fatalf("P column still output wide: %d", got)
					}
				}

				// G toggled off by the user, or unset by its client: only G ends.
				for _, end := range []func(w *Workspace){
					func(w *Workspace) { w.FocusID(1); w.ToggleFullscreen() },
					func(w *Workspace) { w.SetFullscreen(1, false) },
				} {
					w = setup()
					end(w)
					if w.Fullscreen != 0 || !slices.Equal(w.Held, []WindowID{2}) {
						t.Fatalf("G ended: cover %d held %v", w.Fullscreen, w.Held)
					}
					checkGroup(t, w, "G ended")
				}

				// A window outside the group still replaces the cover.
				w = setup()
				w.FocusID(3)
				w.SetFullscreen(3, true)
				if w.Fullscreen != 3 || !slices.Equal(w.Held, []WindowID{2}) {
					t.Fatalf("outsider: cover %d held %v", w.Fullscreen, w.Held)
				}
				checkGroup(t, w, "outsider")
			})
		})
	}
}

// groupPG lays out G (1), P (2) and an unrelated window (3), P held and G
// the active cover, in their own columns (scroll) or bands (cascade).
func groupPG(t *testing.T, overflow Overflow) *Workspace {
	t.Helper()
	w := cols(overflow, 3, 0, []WindowID{1}, []WindowID{2}, []WindowID{3})
	w.Settle(false)
	w.FocusID(2)
	w.SetFullscreen(2, true)
	w.Fullscreen = 0
	w.hold(2)
	w.FocusID(1)
	w.SetFullscreen(1, true)
	if w.Fullscreen != 1 || !slices.Equal(w.Held, []WindowID{2}) {
		t.Fatalf("setup: cover %d held %v", w.Fullscreen, w.Held)
	}
	return w
}

// wantCover checks the cover, the held members, the focus and the view
// settled on the cover.
func wantCover(t *testing.T, w *Workspace, cover WindowID, held []WindowID, step string) {
	t.Helper()
	if w.Fullscreen != cover || !slices.Equal(w.Held, held) || w.left != 0 {
		t.Fatalf("%s: cover %d held %v left %d", step, w.Fullscreen, w.Held, w.left)
	}
	if f, _ := w.Focused(); f != cover || w.Cover() != cover {
		t.Fatalf("%s: focus %d covering %d", step, f, w.Cover())
	}
	if target := w.Engine().ScrollTarget(w.Snapshot()); w.View != target {
		t.Fatalf("%s: view %d not on cover at %d", step, w.View, target)
	}
	checkGroup(t, w, step)
}

// Focus on a held member promotes it; moving back promotes the other.
func TestFullscreenGroupFocusPromotes(t *testing.T) {
	for _, overflow := range []Overflow{OverflowScroll, OverflowCascade} {
		t.Run(string(overflow), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := groupPG(t, overflow)
				toward := func(from, to WindowID) {
					if overflow == OverflowScroll {
						d := 1
						if w.ColumnOf(to) < w.ColumnOf(from) {
							d = -1
						}
						if !w.FocusColumn(d) {
							t.Fatalf("focus column %d failed", d)
						}
						return
					}
					d := 1
					if w.Band(w.ColumnOf(to)) < w.Band(w.ColumnOf(from)) {
						d = -1
					}
					if !w.FocusWindow(d) {
						t.Fatalf("focus window %d failed", d)
					}
				}
				toward(1, 2)
				wantCover(t, w, 2, []WindowID{1}, "to P")
				toward(2, 1)
				wantCover(t, w, 1, []WindowID{2}, "back to G")
				toward(1, 2)
				wantCover(t, w, 2, []WindowID{1}, "to P again")
			})
		})
	}
}

// Taskbar activation of held P promotes it instead of ending G.
func TestFullscreenGroupActivatePromotes(t *testing.T) {
	for _, overflow := range []Overflow{OverflowScroll, OverflowCascade} {
		t.Run(string(overflow), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := groupPG(t, overflow)
				w.Activate(2)
				wantCover(t, w, 2, []WindowID{1}, "activate P")
			})
		})
	}
}

// Toggling a focused member ends only it: focus promoted it first.
func TestFullscreenGroupToggleAfterPromote(t *testing.T) {
	for _, overflow := range []Overflow{OverflowScroll, OverflowCascade} {
		t.Run(string(overflow), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := groupPG(t, overflow)
				w.FocusID(2)
				wantCover(t, w, 2, []WindowID{1}, "focus P")
				w.ToggleFullscreen()
				if w.Fullscreen != 0 || !slices.Equal(w.Held, []WindowID{1}) {
					t.Fatalf("toggle P: cover %d held %v", w.Fullscreen, w.Held)
				}
				checkGroup(t, w, "toggle P")
			})
		})
	}
}

// Focus on a window outside the group: scroll keeps the cover, scrolled
// off; cascade demotes it to held (TestFullscreenGroupCascadeLeaveDemotes).
func TestFullscreenGroupFocusOutsider(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := groupPG(t, OverflowScroll)
		w.FocusID(2) // P, so the unrelated column is the right neighbour
		w.FocusColumn(1)
		if f, _ := w.Focused(); f != 3 || w.Fullscreen != 2 || w.Cover() != 0 || !slices.Equal(w.Held, []WindowID{1}) {
			t.Fatalf("scroll: focus %d cover %d covering %d held %v", f, w.Fullscreen, w.Cover(), w.Held)
		}
	})
}

// Cascade: leaving a group for an unrelated band demotes the cover to held;
// returning to a member promotes it without a toggle.
func TestFullscreenGroupCascadeLeaveDemotes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := groupPG(t, OverflowCascade)
		w.FocusID(1)
		d := 1
		if w.Band(w.ColumnOf(3)) < w.Band(w.ColumnOf(1)) {
			d = -1
		}
		// Stepping through P's band on the way promotes P; any member left
		// for the outsider is demoted.
		for f, _ := w.Focused(); f != 3; f, _ = w.Focused() {
			if !w.FocusWindow(d) {
				t.Fatal("no path to the outsider")
			}
		}
		held := slices.Clone(w.Held)
		slices.Sort(held)
		if w.Fullscreen != 0 || w.Cover() != 0 || w.left != 0 || !slices.Equal(held, []WindowID{1, 2}) {
			t.Fatalf("left group: cover %d covering %d left %d held %v", w.Fullscreen, w.Cover(), w.left, w.Held)
		}
		checkGroup(t, w, "left group")
		ps := w.Layout()
		if p := placementOf(t, ps, 3); p.Hidden || !p.Rect.Overlaps(w.Output) {
			t.Fatalf("outsider not shown: %+v", p)
		}
		for _, id := range []WindowID{1, 2} {
			if p := placementOf(t, ps, id); !p.Fullscreen || p.Rect.W != w.Output.W || p.Rect.H != w.Output.H {
				t.Fatalf("held %d lost fullscreen: %+v", id, p)
			}
		}
		// Back to G: it covers again, P stays held.
		w.FocusID(1)
		wantCover(t, w, 1, []WindowID{2}, "back to G")
	})
}

// Scroll is unaffected: the cover stays set, scrolled off.
func TestFullscreenGroupScrollLeaveKeepsCover(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := groupPG(t, OverflowScroll)
		w.FocusID(3)
		if w.Fullscreen != 1 || w.Cover() != 0 || !slices.Equal(w.Held, []WindowID{2}) || w.left != 0 {
			t.Fatalf("scroll: cover %d covering %d held %v left %d", w.Fullscreen, w.Cover(), w.Held, w.left)
		}
	})
}

// A lone fullscreen window keeps the leave latch in cascade.
func TestFullscreenLoneCascadeLeave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := cols(OverflowCascade, 3, 0, []WindowID{1}, []WindowID{3})
		w.Settle(false)
		w.FocusID(1)
		w.SetFullscreen(1, true)
		d := 1
		if w.Band(w.ColumnOf(3)) < w.Band(w.ColumnOf(1)) {
			d = -1
		}
		if !w.FocusWindow(d) {
			t.Fatal("no move")
		}
		if w.Fullscreen != 0 || w.left != 1 || len(w.Held) != 0 {
			t.Fatalf("lone: cover %d left %d held %v", w.Fullscreen, w.left, w.Held)
		}
	})
}

// Cascade taskbar activation of an unrelated window demotes the group's
// cover like a focus move; a lone cover still ends with the latch.
func TestFullscreenGroupCascadeActivateDemotes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := groupPG(t, OverflowCascade)
		w.Activate(3)
		held := slices.Clone(w.Held)
		slices.Sort(held)
		if f, _ := w.Focused(); f != 3 || w.Fullscreen != 0 || w.Cover() != 0 || w.left != 0 || !slices.Equal(held, []WindowID{1, 2}) {
			t.Fatalf("activate outsider: focus %d cover %d left %d held %v", f, w.Fullscreen, w.left, w.Held)
		}
		checkGroup(t, w, "activate outsider")
		if p := placementOf(t, w.Layout(), 3); p.Hidden {
			t.Fatalf("outsider hidden: %+v", p)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		w := cols(OverflowCascade, 3, 0, []WindowID{1}, []WindowID{3})
		w.Settle(false)
		w.FocusID(1)
		w.SetFullscreen(1, true)
		w.Activate(3)
		if w.Fullscreen != 0 || w.left != 1 || len(w.Held) != 0 {
			t.Fatalf("lone: cover %d left %d held %v", w.Fullscreen, w.left, w.Held)
		}
	})
}

// A held member taken off by a rule arrives fullscreen; the source group
// stays consistent.
func TestFullscreenGroupMoveByRule(t *testing.T) {
	for _, overflow := range []Overflow{OverflowScroll, OverflowCascade} {
		t.Run(string(overflow), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := groupPG(t, overflow)
				to := cols(overflow, 3, 0, []WindowID{9})
				MoveByRule(w, to, 2)
				// The destination's rules decide: cascade refuses an
				// unfocused window's fullscreen (mayCover), as for a cover.
				want := WindowID(2)
				if overflow == OverflowCascade {
					want = 0
				}
				if to.Fullscreen != want || w.Fullscreen != 1 || len(w.Held) != 0 {
					t.Fatalf("dest cover %d, source cover %d held %v", to.Fullscreen, w.Fullscreen, w.Held)
				}
				checkGroup(t, w, "source")
				checkGroup(t, to, "dest")
			})
		})
	}
}

// Focused moves carry the held members of the moved column or tile. Focus
// on a held member promotes it, so the state is set by hand: a held member
// stacked under the moved selection.
func TestFullscreenGroupMoveCarriesHeld(t *testing.T) {
	for _, tc := range []struct {
		name string
		move func(from, to *Workspace) bool
		want WindowID // dest cover
	}{
		{"column", func(f, t *Workspace) bool { return MoveColumnTo(f, t, 0) }, 2},
		{"focused column", func(f, t *Workspace) bool { return MoveFocused(f, t, true) }, 2},
		{"window", func(f, t *Workspace) bool { return MoveWindowTo(f, t, 0) }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := cols(OverflowScroll, 3, 0, []WindowID{1}, []WindowID{2, 4}, []WindowID{3})
				w.Settle(false)
				w.Fullscreen, w.Held = 1, []WindowID{2}
				w.Focus, w.Columns[1].Focus = 1, 1 // 4 selected, 2 stacked above
				to := cols(OverflowScroll, 3, 0, []WindowID{9})
				if !tc.move(w, to) {
					t.Fatal("no move")
				}
				if to.Fullscreen != tc.want {
					t.Fatalf("dest cover %d want %d", to.Fullscreen, tc.want)
				}
				if tc.want != 0 && w.IsHeld(2) {
					t.Fatalf("source still holds 2: %v", w.Held)
				}
				checkGroup(t, w, "source")
				checkGroup(t, to, "dest")
			})
		})
	}
}

// Moving the active member leaves the source with no cover and its held
// members held; the next focus on one of them promotes it.
func TestFullscreenGroupMoveFullscreenKeepsHeld(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := cols(OverflowScroll, 3, 0, []WindowID{1}, []WindowID{3}, []WindowID{2})
		w.Settle(false)
		w.Fullscreen, w.Held = 1, []WindowID{2}
		w.FocusID(1)
		to := cols(OverflowScroll, 3, 0, []WindowID{9})
		if !MoveFullscreen(w, to, 0) {
			t.Fatal("no move")
		}
		if to.Fullscreen != 1 || w.Fullscreen != 0 || !slices.Equal(w.Held, []WindowID{2}) {
			t.Fatalf("dest cover %d, source cover %d held %v", to.Fullscreen, w.Fullscreen, w.Held)
		}
		checkGroup(t, w, "source")
		w.FocusID(2)
		if w.Fullscreen != 2 || len(w.Held) != 0 {
			t.Fatalf("focus held: cover %d held %v", w.Fullscreen, w.Held)
		}
	})
}

// Merged held members stay held where the destination scrolls.
func TestFullscreenGroupMerge(t *testing.T) {
	for _, tc := range []struct {
		overflow Overflow
		held     bool
	}{{OverflowScroll, true}, {OverflowCascade, true}, {OverflowFixed, false}} {
		t.Run(string(tc.overflow), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				src := cols(OverflowScroll, 3, 0, []WindowID{1}, []WindowID{2})
				src.Fullscreen, src.Held = 1, []WindowID{2}
				dst := cols(tc.overflow, 3, 0, []WindowID{9})
				dst.SetOverflow(tc.overflow)
				Merge(dst, src)
				if dst.IsHeld(2) != tc.held {
					t.Fatalf("held %v want %v", dst.Held, tc.held)
				}
				if tc.held && dst.HeldColumn(dst.ColumnOf(2)) && len(dst.Columns[dst.ColumnOf(2)].Windows) != 1 {
					t.Fatalf("held 2 not alone: %v", dst.Columns)
				}
				checkGroup(t, dst, "merge")
			})
		})
	}
}

// A held column, output-sized like the cover, has no resize borders.
func TestHeldColumnNoBorders(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := spanWorkspace(OverflowScroll, 3)
		rs := w.columnRects()
		x, y := rs[0].X+rs[0].W+5, 100
		if _, ok := w.BorderAt(x, y, 6); !ok {
			t.Fatal("no border before hold")
		}
		w.Held = []WindowID{2}
		rs = w.columnRects()
		x = rs[0].X + rs[0].W + 5
		if b, ok := w.BorderAt(x, y, 6); ok {
			t.Fatalf("border %+v next to a held column", b)
		}
		if w.MoveBorder(Border{Kind: BorderColumn, Col: 0}, rs[0].X+rs[0].W-50) {
			t.Fatal("moved a border into a held column")
		}
		if w.MoveBorder(Border{Kind: BorderColumn, Col: 1}, rs[1].X+rs[1].W-50) {
			t.Fatal("moved a held column's border")
		}
	})
}

// Overview selection (LeaveCoverFor) under a pinned group: a held member
// is promoted, an unrelated window demotes the cover, a lone cover leaves.
func TestFullscreenGroupLeaveCoverFor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target WindowID
		cover  WindowID
		held   []WindowID
	}{
		{"held member", 2, 2, []WindowID{1}},
		{"unrelated", 3, 0, []WindowID{1, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := groupPG(t, OverflowCascade)
				w.LeaveCoverFor(tc.target)
				w.FocusID(tc.target) // the overview then focuses it
				held := slices.Clone(w.Held)
				slices.Sort(held)
				if w.Fullscreen != tc.cover || !slices.Equal(held, tc.held) || w.left != 0 {
					t.Fatalf("cover %d held %v left %d", w.Fullscreen, w.Held, w.left)
				}
				if f, _ := w.Focused(); f != tc.target {
					t.Fatalf("focus %d", f)
				}
				checkGroup(t, w, tc.name)
			})
		})
	}
	t.Run("lone", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := cols(OverflowCascade, 3, 0, []WindowID{1}, []WindowID{3})
			w.Settle(false)
			w.FocusID(1)
			w.SetFullscreen(1, true)
			w.LeaveCoverFor(3)
			if w.Fullscreen != 0 || w.left != 1 || len(w.Held) != 0 {
				t.Fatalf("lone: cover %d left %d held %v", w.Fullscreen, w.left, w.Held)
			}
		})
	})
}

// Unstashing an unrelated entry shown over a cascade group's cover keeps
// the group: both members held, no latch; focus on G promotes it.
func TestFullscreenGroupUnstashOverCover(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := groupPG(t, OverflowCascade)
		// Stash the unrelated window 3, then show the stash over G.
		w.Fullscreen, w.Held = 0, []WindowID{1, 2}
		w.FocusID(3)
		w.ToggleWindowStash()
		w.FocusID(1)
		if w.Fullscreen != 1 || w.StashIndex(3) < 0 {
			t.Fatalf("setup: cover %d stash %d", w.Fullscreen, w.StashIndex(3))
		}
		w.Activate(3)
		if !w.stashOverCover() {
			t.Fatal("setup: stash not over the cover")
		}
		w.ToggleWindowStash()
		held := slices.Clone(w.Held)
		slices.Sort(held)
		if w.StashIndex(3) >= 0 || w.Fullscreen != 0 || !slices.Equal(held, []WindowID{1, 2}) || w.left != 0 {
			t.Fatalf("unstash: stash %d cover %d held %v left %d", w.StashIndex(3), w.Fullscreen, w.Held, w.left)
		}
		if f, _ := w.Focused(); f != 3 {
			t.Fatalf("focus %d", f)
		}
		checkGroup(t, w, "unstash")
		w.FocusID(1)
		if w.Fullscreen != 1 || w.left != 0 {
			t.Fatalf("refocus: cover %d left %d", w.Fullscreen, w.left)
		}
	})
}

// A modal dialog of an unrelated window hidden under a cascade group's
// cover, reached by any focus path, shows focused; the group survives
// (held members, no latch).
func TestFullscreenGroupModalUnderCover(t *testing.T) {
	for _, tc := range []struct {
		name  string
		focus func(w *Workspace)
	}{
		{"activate parent", func(w *Workspace) { w.Activate(3) }},
		{"activate dialog", func(w *Workspace) { w.Activate(7) }},
		{"click parent", func(w *Workspace) { w.Click(3) }},
		{"focus parent", func(w *Workspace) { w.FocusID(3); w.FocusModal(0) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := groupPG(t, OverflowCascade)
				w.AddDialog(7, 3, 100, 50)
				w.SetModal(7, true)
				w.FocusID(1)
				if w.Cover() != 1 || !slices.Equal(w.Held, []WindowID{2}) {
					t.Fatalf("setup: cover %d held %v", w.Cover(), w.Held)
				}
				tc.focus(w)
				if f, _ := w.Focused(); f != 7 {
					t.Fatalf("focus %d, want the dialog", f)
				}
				if p := placementOf(t, w.Layout(), 7); p.Hidden {
					t.Fatalf("dialog hidden: %+v", p)
				}
				// Activate demotes the cover (groupLeaveTo); a plain focus
				// move scrolls it off, still the group's active member.
				if !w.IsFullscreenMember(1) || !w.IsFullscreenMember(2) || w.left != 0 || w.Cover() != 0 {
					t.Fatalf("cover %d covering %d held %v left %d", w.Fullscreen, w.Cover(), w.Held, w.left)
				}
				checkGroup(t, w, tc.name)
			})
		})
	}
}
