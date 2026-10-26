package core

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/ports"
)

// stashCoverCore has two screens; the first holds tile 1 and the hidden
// stash [2 3], 3 selected, under a covering fullscreen window: tile 1, or
// float 4 with float set.
func stashCoverCore(t *testing.T, overflow Overflow, float bool) (*Core, *Workspace, WindowID) {
	t.Helper()
	cfg := ports.Config{}
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.Overflow = string(overflow)
	cfg.Layout.MaxColumns = 2
	c, err := New(cfg, Channels{Scenes: make(chan []ports.Scene, 1)}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c.addScreen(ports.OutputInfo{Name: "A", Width: 100, Height: 80})
	c.addScreen(ports.OutputInfo{Name: "B", Width: 100, Height: 80})
	m := c.screens[0].mon
	for id := WindowID(1); id <= 3; id++ {
		m.AddWindow(id)
	}
	w := m.Current()
	for _, id := range []WindowID{2, 3} {
		w.FocusID(id)
		w.ToggleWindowStash()
	}
	c.applyAction(ActionToggleStashVisible) // hidden
	cover := WindowID(1)
	if float {
		cover = 4
		m.AddFloating(4, 100, 80)
		m.SetFullscreen(4, true)
	} else {
		c.applyAction(ActionToggleFullscreen)
	}
	if w.cover() != cover || c.focusScreen != 0 {
		t.Fatalf("setup: cover %d", w.cover())
	}
	return c, w, cover
}

// toggle-stash-visible is a user override: under any covering fullscreen
// window it shows the stash on top, focused, the window fullscreen
// behind. Moves stay in the stash, at its edges too. Toggling again
// hides it, back on the fullscreen window.
func TestStashShowsOverCover(t *testing.T) {
	for _, tc := range []struct {
		name     string
		overflow Overflow
		float    bool
	}{
		{"fixed tile", OverflowFixed, false},
		{"scroll tile", OverflowScroll, false},
		{"fixed float", OverflowFixed, true},
		{"scroll float", OverflowScroll, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, w, cover := stashCoverCore(t, tc.overflow, tc.float)
				c.applyAction(ActionToggleStashVisible)
				ps := w.Layout()
				if id, _ := w.Focused(); id != 3 || w.cover() != cover {
					t.Fatalf("focused %d cover %d", id, w.cover())
				}
				if p := previewOf(t, ps, cover); p.Hidden || !p.Fullscreen {
					t.Fatalf("cover %+v", p)
				}
				if p := previewOf(t, ps, 3); p.Hidden || ps[len(ps)-1].ID != 3 && ps[len(ps)-2].ID != 3 {
					t.Fatalf("stash not on top: %+v", ps)
				}
				c.applyAction(ActionFocusColumnLeft)
				if id, _ := w.Focused(); id != 2 || w.cover() != cover {
					t.Fatalf("left: focused %d cover %d", id, w.cover())
				}
				c.applyAction(ActionFocusColumnRight)
				c.applyAction(ActionFocusColumnRight) // stash edge: stays, like a shown stash
				if id, _ := w.Focused(); id != 3 || c.focusScreen != 0 {
					t.Fatalf("edge: focused %d screen %d", id, c.focusScreen)
				}
				c.applyAction(ActionToggleStashVisible)
				if id, _ := w.Focused(); id != cover || w.cover() != cover || !previewOf(t, w.Layout(), 3).Hidden {
					t.Fatalf("hide: focused %d cover %d", id, w.cover())
				}

			})
		})
	}
}

// Only the user shows the stash over the cover: a window mapping
// meanwhile is not captured, a dialog of the cover shows above the
// stash, and the override ends with its cover.
func TestStashOverCoverEdges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, w, cover := stashCoverCore(t, OverflowFixed, false)
		m := c.screens[0].mon
		m.SetStashCapture(true)
		c.applyAction(ActionToggleStashVisible)
		m.AddWindow(5)
		if w.stashIndex(5) >= 0 || !previewOf(t, w.Layout(), 5).Hidden {
			t.Fatalf("new window shown: %+v", w.Layout())
		}
		w.AddDialog(6, cover, 20, 20)
		if ps := w.Layout(); ps[len(ps)-1].ID != 6 {
			t.Fatalf("dialog under the stash: %+v", ps)
		}
		if id, _ := w.Focused(); id != 6 {
			t.Fatalf("dialog focus %d", id)
		}
		c.applyAction(ActionFocusColumnLeft) // back to the stash
		if id, _ := w.Focused(); id != 3 || !w.stashOverCover() {
			t.Fatalf("from dialog: focused %d", id)
		}
		w.RemoveWindow(6)
		// The cover closes: the stash it showed over is a plain shown stash;
		// a later fullscreen window is never covered by it.
		w.RemoveWindow(cover)
		if w.stashOver != 0 {
			t.Fatal("override outlived its cover")
		}
		w.FocusID(5)
		w.ToggleFullscreen()
		if p := previewOf(t, w.Layout(), 3); !p.Hidden {
			t.Fatalf("stash over a new cover: %+v", p)
		}

	})
}

// Another window's fullscreen request ends the override: the old cover
// asking again never brings the stash back over it.
func TestStashOverCoverOtherRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, w, cover := stashCoverCore(t, OverflowScroll, false)
		m := c.screens[0].mon
		m.AddWindow(5)
		c.applyAction(ActionToggleStashVisible)
		m.SetFullscreen(5, true)
		m.SetFullscreen(cover, true)
		if w.cover() != cover || w.stashOverCover() {
			t.Fatalf("cover %d, override revived", w.cover())
		}

	})
}

// Acting on a stashed window over the cover leaves the cover for it:
// toggle-window-stash returns it to a visible column, toggle-fullscreen
// makes it the fullscreen window.
func TestStashOverCoverLeaves(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, w, _ := stashCoverCore(t, OverflowFixed, false)
		c.applyAction(ActionToggleStashVisible)
		c.applyAction(ActionToggleWindowStash)
		if id, _ := w.Focused(); id != 3 || w.Fullscreen != 0 || previewOf(t, w.Layout(), 3).Hidden {
			t.Fatalf("unstash: focused %d fullscreen %d", id, w.Fullscreen)
		}
		c, w, _ = stashCoverCore(t, OverflowFixed, false)
		c.applyAction(ActionToggleStashVisible)
		c.applyAction(ActionToggleFullscreen)
		if w.cover() != 3 || w.left != 1 {
			t.Fatalf("fullscreen: cover %d left %d", w.cover(), w.left)
		}

	})
}

// Activating a window over a covering fullscreen window (xdg-activation,
// taskbar) never ends that fullscreen when the window can show over it:
// a stashed window shows the stash over the cover, and dialogs of the
// cover or of stashed windows, and their dialogs, show above. A game
// told it left fullscreen (configure 0x0) quits.
func TestActivateOverCover(t *testing.T) {
	covers := []struct {
		name     string
		overflow Overflow
		float    bool
	}{
		{"fixed tile", OverflowFixed, false},
		{"scroll tile", OverflowScroll, false},
		{"fixed float", OverflowFixed, true},
		{"scroll float", OverflowScroll, true},
	}
	cases := []struct {
		name string
		// setup prepares windows and returns the window to activate.
		setup func(w *Workspace, cover WindowID) WindowID
		// focused is the window focused after the activation.
		focused WindowID
		// over reports whether the stash shows over the cover then.
		over bool
	}{
		{"stashed", func(*Workspace, WindowID) WindowID { return 2 }, 2, true},
		{"modal of stashed", func(w *Workspace, _ WindowID) WindowID {
			w.AddDialog(9, 2, 20, 10)
			w.SetModal(9, true)
			return 2
		}, 9, true},
		{"modal chain of stashed", func(w *Workspace, _ WindowID) WindowID {
			w.AddDialog(9, 2, 20, 10)
			w.SetModal(9, true)
			w.AddDialog(8, 9, 10, 5)
			w.SetModal(8, true)
			return 2
		}, 8, true},
		{"dialog of stashed", func(w *Workspace, _ WindowID) WindowID {
			w.AddDialog(9, 2, 20, 10)
			return 9
		}, 9, true},
		// A dialog filling the usable area is a covering float: it stays
		// over the stash all the same.
		{"full-size dialog of stashed", func(w *Workspace, _ WindowID) WindowID {
			w.AddDialog(9, 2, 100, 80)
			return 9
		}, 9, true},
		{"dialog of cover", func(w *Workspace, cover WindowID) WindowID {
			w.AddDialog(9, cover, 20, 10)
			w.FocusID(cover)
			return 9
		}, 9, false},
		{"modal chain of cover", func(w *Workspace, cover WindowID) WindowID {
			w.AddDialog(9, cover, 20, 10)
			w.SetModal(9, true)
			w.AddDialog(8, 9, 10, 5)
			w.SetModal(8, true)
			w.FocusID(cover)
			return cover
		}, 8, false},
		{"stashed from a focused cover dialog", func(w *Workspace, cover WindowID) WindowID {
			w.ToggleStashVisible()
			w.AddDialog(9, cover, 20, 10)
			w.FocusID(9)
			return 2
		}, 2, true},
	}
	for _, cv := range covers {
		for _, tc := range cases {
			t.Run(cv.name+"/"+tc.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					_, w, cover := stashCoverCore(t, cv.overflow, cv.float)
					id := tc.setup(w, cover)
					// A dialog of the hidden stash waits hidden under the cover.
					if i := slices.IndexFunc(w.Floats, func(f Float) bool { return f.over == 2 }); i >= 0 && !w.stashOverCover() {
						if p := previewOf(t, w.Layout(), w.Floats[i].ID); !p.Hidden {
							t.Fatalf("dialog of the hidden stash shown before activation: %+v", p)
						}
					}
					view := w.View
					w.Activate(id)
					if got, _ := w.Focused(); got != tc.focused || w.cover() != cover || w.left != 0 || w.stashOverCover() != tc.over {
						t.Fatalf("focused %d (want %d) cover %d left %d over %v", got, tc.focused, w.cover(), w.left, w.stashOverCover())
					}
					ps := w.Layout()
					if p := previewOf(t, ps, cover); p.Hidden || !p.Fullscreen {
						t.Fatalf("cover %+v", p)
					}
					f := slices.IndexFunc(ps, isID(tc.focused))
					if f < slices.IndexFunc(ps, isID(cover)) || ps[f].Hidden {
						t.Fatalf("focused window not over the cover: %+v", ps)
					}
					if tc.over && f < slices.IndexFunc(ps, isID(2)) {
						t.Fatalf("focused window under the stash: %+v", ps)
					}
					if !cv.float && w.View != view {
						t.Fatalf("view %d, was %d", w.View, view)
					}

				})
			})
		}
	}
}

// After an activation shows the stash over the cover, hiding the stash
// or activating the cover returns to it; a dialog of a stashed window,
// modal or mapping meanwhile, shows with the focus and closing it
// returns to the stash over the cover.
func TestActivateOverCoverReturns(t *testing.T) {
	for _, cv := range []struct {
		name     string
		overflow Overflow
		float    bool
	}{
		{"fixed tile", OverflowFixed, false},
		{"scroll tile", OverflowScroll, false},
		{"fixed float", OverflowFixed, true},
		{"scroll float", OverflowScroll, true},
	} {
		t.Run(cv.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, w, cover := stashCoverCore(t, cv.overflow, cv.float)
				w.Activate(2)
				w.ToggleStashVisible()
				if id, _ := w.Focused(); id != cover || w.stashShown() || w.Fullscreen != cover {
					t.Fatalf("hide: focused %d shown %v fullscreen %d", id, w.stashShown(), w.Fullscreen)
				}
				w.Activate(2)
				w.Activate(cover)
				if id, _ := w.Focused(); id != cover || w.stashShown() || w.Fullscreen != cover {
					t.Fatalf("activate cover: focused %d shown %v fullscreen %d", id, w.stashShown(), w.Fullscreen)
				}
				w.AddDialog(7, 2, 20, 10)
				w.SetModal(7, true)
				w.FocusID(cover)
				w.Activate(2)
				w.RemoveWindow(7)
				if id, _ := w.Focused(); id != 2 || !w.stashOverCover() {
					t.Fatalf("modal closed: focused %d over %v", id, w.stashOverCover())
				}
				w.AddDialog(9, 2, 20, 10)
				w.AddDialog(8, 9, 10, 5)
				if id, _ := w.Focused(); id != 8 || previewOf(t, w.Layout(), 8).Hidden {
					t.Fatalf("mapped chain: focused %d", id)
				}
				w.RemoveWindow(8)
				w.RemoveWindow(9)
				if id, _ := w.Focused(); id != 2 || !w.stashOverCover() {
					t.Fatalf("closed: focused %d over %v", id, w.stashOverCover())
				}
				// The cover closes: the stash stays shown and focused, the
				// override ends with it.
				w.RemoveWindow(cover)
				if id, _ := w.Focused(); id != 2 || w.stashOver != 0 || !w.stashShown() {
					t.Fatalf("cover closed: focused %d over %d shown %v", id, w.stashOver, w.stashShown())
				}

			})
		})
	}
}

// Activating a dialog that cannot show over the cover leaves fullscreen
// as any other window does; a dialog of a stashed window whose own
// fullscreen waited while hidden brings that window fullscreen, as
// activating the window itself does.
func TestActivateDialogLeavesCover(t *testing.T) {
	t.Run("dialog of a plain float", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			_, w, cover := stashCoverCore(t, OverflowFixed, false)
			w.AddFloating(5, 20, 10)
			w.AddDialog(9, 5, 10, 5)
			w.FocusID(cover)
			w.Activate(9)
			if id, _ := w.Focused(); id != 9 || w.Fullscreen != 0 || w.left != cover {
				t.Fatalf("focused %d fullscreen %d left %d", id, w.Fullscreen, w.left)
			}

		})
	})
	t.Run("dialog of another tile", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			_, w, cover := stashCoverCore(t, OverflowFixed, true)
			w.AddDialog(9, 1, 10, 5)
			w.FocusID(cover)
			w.Activate(9)
			if id, _ := w.Focused(); id != 9 || w.Fullscreen != 0 || w.left != cover {
				t.Fatalf("focused %d fullscreen %d left %d", id, w.Fullscreen, w.left)
			}

		})
	})
	t.Run("dialog of a stashed window with a pending fullscreen", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			_, w, cover := stashCoverCore(t, OverflowFixed, true)
			w.SetFullscreen(2, true)
			w.AddDialog(9, 2, 10, 5)
			w.FocusID(cover)
			w.Activate(9)
			if id, _ := w.Focused(); id != 9 || w.Fullscreen != 2 || w.stashHidden || w.left != cover {
				t.Fatalf("focused %d fullscreen %d hidden %v left %d", id, w.Fullscreen, w.stashHidden, w.left)
			}
			if p := previewOf(t, w.Layout(), 9); p.Hidden {
				t.Fatalf("dialog hidden: %+v", p)
			}

		})
	})
}

// A dialog parent loop under a cover ends: neither dialog shows over it.
func TestOverCoverDialogLoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, w, _ := stashCoverCore(t, OverflowFixed, true)
		w.AddFloating(8, 10, 5)
		w.AddFloating(9, 10, 5)
		w.SetDialogParent(8, 9)
		w.SetDialogParent(9, 8)
		ps := w.Layout()
		if !previewOf(t, ps, 8).Hidden || !previewOf(t, ps, 9).Hidden {
			t.Fatalf("looped dialogs shown: %+v", ps)
		}

	})
}

// Through core: activating a stashed window keeps a fullscreen game's
// configure fullscreen at the output size.
func TestCoreActivateStashKeepsGameConfigure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := ports.Config{}
		cfg.Keyboard.CmdKey = "super"
		cfg.Layout.MaxColumns = 2
		commands := make(chan ports.ClientCommand, 256)
		c, err := New(cfg, Channels{Commands: commands, Scenes: make(chan []ports.Scene, 1)}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		c.addScreen(ports.OutputInfo{Name: "A", Width: 100, Height: 80})
		m := c.screens[0].mon
		m.AddWindow(2)
		w := m.Current()
		w.ToggleWindowStash()
		w.ToggleStashVisible()
		m.AddFloating(4, 100, 80)
		m.SetFullscreen(4, true)
		ctx := context.Background()
		if err := c.activate(ctx, 2); err != nil {
			t.Fatal(err)
		}
		if err := c.publish(ctx); err != nil {
			t.Fatal(err)
		}
		var last *ports.ConfigureWindow
		for len(commands) > 0 {
			if v, ok := (<-commands).(ports.ConfigureWindow); ok && v.ID == 4 {
				last = &v
			}
		}
		if last == nil || !last.Fullscreen || last.Width != 100 || last.Height != 80 {
			t.Fatalf("game configure %+v", last)
		}

	})
}

func isID(id WindowID) func(Placement) bool {
	return func(p Placement) bool { return p.ID == id }
}
