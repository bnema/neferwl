package workspace

import (
	"testing"
	"testing/synctest"
)

// A window opened by a stash spawn is shown, selected and focused in the
// stash whatever fullscreen state the workspace and the stash are in.
func TestStashSpawnedAlwaysShown(t *testing.T) {
	// base has tile 1 and stash [2 3], 3 selected, the stash shown.
	base := func() *Workspace {
		w := workspace()
		w.AddWindow(1)
		w.AddWindow(2)
		w.AddWindow(3)
		w.FocusID(2)
		w.ToggleWindowStash()
		w.FocusID(3)
		w.ToggleWindowStash()
		return w
	}
	shown := func(t *testing.T, w *Workspace) {
		t.Helper()
		if id, _ := w.Focused(); id != 5 {
			t.Fatalf("focused %d, want 5", id)
		}
		if p := previewOf(t, w.Layout(), 5); p.Hidden || !p.Focused {
			t.Fatalf("new window %+v", p)
		}
		if w.Stash[w.stashAt].ID != 5 {
			t.Fatalf("selected %d", w.Stash[w.stashAt].ID)
		}
	}

	t.Run("covering tile outside the stash", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := base()
			w.FocusID(1)
			w.ToggleFullscreen()
			w.StashSpawned(5)
			shown(t, w)
			if w.Cover() != 1 || !w.stashOverCover() {
				t.Fatalf("cover %d: the tile must stay fullscreen under the stash", w.Cover())
			}
			if p := previewOf(t, w.Layout(), 1); !p.Fullscreen || p.Hidden {
				t.Fatalf("tile %+v", p)
			}
			w.ToggleStashVisible()
			if id, _ := w.Focused(); id != 1 || w.Cover() != 1 {
				t.Fatalf("hidden stash: focused %d cover %d, want the tile", id, w.Cover())
			}

		})
	})

	t.Run("fullscreen stash member", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := base()
			w.ToggleFullscreen() // 3
			if w.Cover() != 3 {
				t.Fatalf("setup: cover %d", w.Cover())
			}
			w.StashSpawned(5)
			shown(t, w)
			if w.Cover() != 0 {
				t.Fatalf("cover %d: the stash member still covers", w.Cover())
			}
			if p := previewOf(t, w.Layout(), 3); p.Fullscreen {
				t.Fatalf("old member %+v", p)
			}

		})
	})

	t.Run("fullscreen waiting for the stash", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := base()
			w.ToggleStashVisible() // hidden
			w.SetFullscreen(3, true)
			if w.hiddenFullscreen != 3 {
				t.Fatalf("setup: deferred %d", w.hiddenFullscreen)
			}
			w.StashSpawned(5)
			shown(t, w)
			if w.Cover() != 0 || w.hiddenFullscreen != 0 {
				t.Fatalf("cover %d, deferred %d", w.Cover(), w.hiddenFullscreen)
			}
			w.ToggleStashVisible()
			w.ToggleStashVisible()
			shown(t, w)

		})
	})
}
