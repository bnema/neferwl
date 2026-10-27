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
