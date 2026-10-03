package core

import (
	"testing"

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
	c, err := New(cfg, Channels{Scenes: make(chan []ports.Scene, 1)})
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
	}
}

// Only the user shows the stash over the cover: a window mapping
// meanwhile is not captured, a dialog of the cover shows above the
// stash, and the override ends with its cover.
func TestStashOverCoverEdges(t *testing.T) {
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
}

// Acting on a stashed window over the cover leaves the cover for it:
// toggle-window-stash returns it to a visible column, toggle-fullscreen
// makes it the fullscreen window.
func TestStashOverCoverLeaves(t *testing.T) {
	c, w, _ := stashCoverCore(t, OverflowFixed, false)
	c.applyAction(ActionToggleStashVisible)
	c.applyAction(ActionToggleWindowStash)
	if id, _ := w.Focused(); id != 3 || w.fullscreen != 0 || previewOf(t, w.Layout(), 3).Hidden {
		t.Fatalf("unstash: focused %d fullscreen %d", id, w.fullscreen)
	}
	c, w, _ = stashCoverCore(t, OverflowFixed, false)
	c.applyAction(ActionToggleStashVisible)
	c.applyAction(ActionToggleFullscreen)
	if w.cover() != 3 || w.left != 1 {
		t.Fatalf("fullscreen: cover %d left %d", w.cover(), w.left)
	}
}
