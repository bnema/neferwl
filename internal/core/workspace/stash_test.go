package workspace

import (
	"testing"
	"testing/synctest"
)

// A fullscreen request made while the stash was hidden applies on show,
// even over an off-screen fullscreen tile.
func TestStashPendingFullscreenOverTile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := workspace()
		w.SetMaxColumns(1)
		w.AddWindow(1)
		w.AddWindow(2)
		w.AddWindow(3)
		w.ToggleWindowStash() // 3 stashed
		w.ToggleStashVisible()
		w.SetFullscreen(3, true) // waits
		w.FocusID(1)
		w.SetFullscreen(1, true)
		w.FocusID(2) // 1 scrolled off: it no longer covers
		if w.Cover() != 0 {
			t.Fatal("tile still covers")
		}
		w.ToggleStashVisible()
		if w.Fullscreen != 3 || w.hiddenFullscreen != 0 {
			t.Fatalf("fullscreen %d pending %d", w.Fullscreen, w.hiddenFullscreen)
		}
		if id, _ := w.Focused(); id != 3 {
			t.Fatalf("focused %d", id)
		}

	})
}

// Activating a hidden stashed window over a scroll fullscreen tile shows
// the stash over it: the tile stays fullscreen and the view stays aligned.
func TestActivateHiddenStashOverScrollFullscreen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := workspace()
		w.AddWindow(1)
		w.AddWindow(2)
		w.AddWindow(3)
		w.ToggleWindowStash() // 3 stashed
		w.ToggleStashVisible()
		w.FocusID(2)
		w.SetFullscreen(2, true)
		w.Activate(3)
		if id, _ := w.Focused(); id != 3 || w.Fullscreen != 2 || !w.stashOverCover() {
			t.Fatalf("focused %d fullscreen %d over %v", id, w.Fullscreen, w.stashOverCover())
		}
		view := w.View
		w.scroll()
		if w.View != view {
			t.Fatalf("view %d, settled %d", view, w.View)
		}

	})
}

func TestFullscreenStashMemberReturns(t *testing.T) {
	for _, kind := range []string{"captured", "former column", "free float"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := workspace()
				w.AddWindow(1)
				w.ToggleWindowStash()
				if kind == "captured" {
					if !w.CaptureStash(2) {
						t.Fatal("not captured")
					}
				} else {
					w.AddWindow(2)
					w.FocusID(2)
					if kind == "free float" {
						w.Apply(ActionToggleFloating)
					}
					w.ToggleWindowStash()
				}
				w.SetFullscreen(2, true)
				if w.Cover() != 2 {
					t.Fatal("setup has no cover")
				}
				w.ToggleWindowStash()
				if w.StashIndex(2) >= 0 || w.Fullscreen != 2 || w.Cover() != 2 || w.stashFocus || w.hiddenFullscreen != 0 || w.stashOver != 0 {
					t.Fatalf("bad restored state: %+v", w)
				}
				if id, _ := w.Focused(); id != 2 {
					t.Fatalf("focused %d", id)
				}
				if kind == "free float" {
					if i := w.floatIndex(2); i < 0 || !w.Floats[i].free {
						t.Fatal("free float not restored")
					}
				} else if len(w.Columns) != 1 || w.Columns[0].Windows[0] != 2 {
					t.Fatal(w.Columns)
				}
				for _, p := range w.Layout() {
					if p.ID == 2 && (!p.Fullscreen || p.Hidden || p.Rect != w.Output) {
						t.Fatalf("placement %+v", p)
					}
				}
			})
		})
	}
}

func TestFullscreenOutsideStashKeepsPlace(t *testing.T) {
	for _, floating := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			w := workspace()
			if floating {
				w.AddFloating(1, 100, 80)
			} else {
				w.AddWindow(1)
			}
			w.SetFullscreen(1, true)
			w.ToggleWindowStash()
			if w.StashIndex(1) >= 0 || w.Fullscreen != 1 || w.Cover() != 1 {
				t.Fatal("fullscreen window moved")
			}
		})
	}
}
