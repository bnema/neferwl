package core

import (
	"slices"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// twoSettled maps two tiles with animations on and settles them.
func twoSettled(t *testing.T) (*Core, *indicatorClock, chan ports.ClientCommand, *screen) {
	t.Helper()
	c, ic, cmds := fadeCore(t)
	c.cfg.Animations.On = true
	sc := c.cur()
	c.mapWindow(ports.WindowMapped{ID: 1})
	c.mapWindow(ports.WindowMapped{ID: 2})
	indicatorScene(t, c)
	ic.now = ic.now.Add(5 * time.Second)
	c.animate(ic.now, nil)
	indicatorScene(t, c)
	if len(sc.rects) != 0 {
		t.Fatalf("motions left after the settle: %d", len(sc.rects))
	}
	drainConfigures(cmds)
	return c, ic, cmds, sc
}

// A tiled unmap: the closed window stays drawn as a leaving entry from its
// settled rect, fading out and shrinking, hidden to input and configures;
// its neighbour slides from where it was to the whole frame; the client is
// configured once; everything settles exactly and no motion is left.
func TestUnmapTiledLeavesAndReflows(t *testing.T) {
	c, ic, cmds, sc := twoSettled(t)
	old1 := sceneWindow(t, indicatorScene(t, c), 1).Rect
	left := settledOf(t, sc, 2)

	c.unmapWindow(ports.WindowUnmapped{ID: 2})
	s := indicatorScene(t, c)
	w2 := sceneWindow(t, s, 2)
	if w2.Hidden || w2.Fade != 0 || w2.Rect != left.Rect || w2.Zoom != 0 {
		t.Fatalf("closed window right after the unmap: %+v, want drawn opaque at %+v", w2, left.Rect)
	}
	if got := sceneWindow(t, s, 1).Rect; got != old1 {
		t.Fatalf("neighbour right after the unmap at %+v, want the old %+v", got, old1)
	}
	settled1 := settledOf(t, sc, 1)
	if settled1.Rect == old1 {
		t.Fatal("the closed column did not move the neighbour: the case checks nothing")
	}
	if ids := drainConfigures(cmds); !slices.Equal(ids, []WindowID{1}) {
		t.Fatalf("configures %v, want one for 1 only", ids)
	}
	if _, w := c.screenOf(2); w != nil {
		t.Fatal("the unmapped window is still in a workspace")
	}
	cx, cy := float64(left.Rect.X+left.Rect.W/2), float64(left.Rect.Y+left.Rect.H/2)
	if id, _, _ := c.hit(cx, cy); id == 2 {
		t.Fatal("the leaving window is hit")
	}

	ic.now = ic.now.Add(40 * time.Millisecond)
	c.animate(ic.now, nil)
	s = indicatorScene(t, c)
	w2 = sceneWindow(t, s, 2)
	if !(w2.Fade > 0 && w2.Fade < 1) || w2.Rect.W >= left.Rect.W || !(w2.Zoom > appearScale && w2.Zoom < 1) {
		t.Fatalf("first frame of the leaving window: %+v", w2)
	}
	if r := sceneWindow(t, s, 1).Rect; !(r.W > old1.W && r.W < settled1.Rect.W) {
		t.Fatalf("neighbour on the first frame %+v, want between %+v and %+v", r, old1, settled1.Rect)
	}
	if ids := drainConfigures(cmds); len(ids) != 0 {
		t.Fatalf("frames reconfigured %v", ids)
	}

	ic.now = ic.now.Add(5 * time.Second)
	c.animate(ic.now, nil)
	s = indicatorScene(t, c)
	if slices.ContainsFunc(s.Windows, func(w ports.SceneWindow) bool { return w.ID == 2 }) {
		t.Fatalf("the closed window is still in the scene: %+v", s.Windows)
	}
	if w := sceneWindow(t, s, 1); w.Rect != settled1.Rect {
		t.Fatalf("neighbour settled at %+v, want %+v", w.Rect, settled1.Rect)
	}
	if len(sc.rects) != 0 || c.animating() {
		t.Fatal("still animating after the settle")
	}
}

// A float leaves too, from its settled rect.
func TestUnmapFloatingLeaves(t *testing.T) {
	c, ic, _ := fadeCore(t)
	c.cfg.Animations.On = true
	sc := c.cur()
	c.mapWindow(ports.WindowMapped{ID: 1})
	c.mapWindow(ports.WindowMapped{ID: 2, Floating: true, Width: 80, Height: 60})
	indicatorScene(t, c)
	ic.now = ic.now.Add(5 * time.Second)
	c.animate(ic.now, nil)
	indicatorScene(t, c)
	left := settledOf(t, sc, 2)
	c.unmapWindow(ports.WindowUnmapped{ID: 2})
	s := indicatorScene(t, c)
	if w := sceneWindow(t, s, 2); !w.Floating || w.Fade != 0 || w.Rect != left.Rect {
		t.Fatalf("float right after the unmap: %+v, settled %+v", w, left.Rect)
	}
	ic.now = ic.now.Add(5 * time.Second)
	c.animate(ic.now, nil)
	if s := indicatorScene(t, c); slices.ContainsFunc(s.Windows, func(w ports.SceneWindow) bool { return w.ID == 2 }) {
		t.Fatalf("the closed float is still in the scene: %+v", s.Windows)
	}
}

// A window closed while its own rect motion runs leaves from the rect it
// was going to, its motion replaced by the leaving one.
func TestUnmapMidMotionLeavesFromSettled(t *testing.T) {
	c, ic, _, sc := twoSettled(t)
	c.mapWindow(ports.WindowMapped{ID: 3})
	indicatorScene(t, c)
	ic.now = ic.now.Add(30 * time.Millisecond)
	c.animate(ic.now, nil)
	indicatorScene(t, c)
	settled3 := settledOf(t, sc, 3)
	if drawn := sceneWindow(t, indicatorScene(t, c), 3).Rect; drawn == settled3.Rect {
		t.Fatal("window 3 is not in flight: the case checks nothing")
	}
	c.unmapWindow(ports.WindowUnmapped{ID: 3})
	s := indicatorScene(t, c)
	w := sceneWindow(t, s, 3)
	if w.Rect != settled3.Rect || w.Fade != 0 {
		t.Fatalf("leaving window right after the unmap: %+v, want its settled %+v", w, settled3.Rect)
	}
	if rm, ok := sc.rects[3]; !ok || !rm.leaving {
		t.Fatalf("no leaving motion for window 3: %+v", rm)
	}
}

// With animations off an unmap is instant: the window is gone, no motion.
func TestUnmapOffIsInstant(t *testing.T) {
	c, _, _ := fadeCore(t)
	sc := c.cur()
	c.mapWindow(ports.WindowMapped{ID: 1})
	c.mapWindow(ports.WindowMapped{ID: 2})
	indicatorScene(t, c)
	c.unmapWindow(ports.WindowUnmapped{ID: 2})
	s := indicatorScene(t, c)
	if slices.ContainsFunc(s.Windows, func(w ports.SceneWindow) bool { return w.ID == 2 }) || len(sc.rects) != 0 || c.animating() {
		t.Fatalf("an unmap animated with animations off: %+v", s.Windows)
	}
	if w := sceneWindow(t, s, 1); w.Rect != settledOf(t, sc, 1).Rect {
		t.Fatalf("neighbour not at its settled rect: %+v", w)
	}
}

// Windows that are not drawn as a tile or an ordinary float do not leave:
// fullscreen, on another workspace, under a session lock, with the overview
// open. Nothing is left of them.
func TestUnmapExceptions(t *testing.T) {
	gone := func(t *testing.T, c *Core, sc *screen, id WindowID) {
		t.Helper()
		s := indicatorScene(t, c)
		if slices.ContainsFunc(s.Windows, func(w ports.SceneWindow) bool { return w.ID == id }) || len(sc.rects) != 0 {
			t.Fatalf("window %d left something behind: %+v rects %+v", id, s.Windows, sc.rects)
		}
		if _, w := c.screenOf(id); w != nil {
			t.Fatalf("window %d still placed", id)
		}
	}
	t.Run("fullscreen", func(t *testing.T) {
		c, _, _, sc := twoSettled(t)
		sc.mon.SetFullscreen(2, true)
		indicatorScene(t, c)
		c.unmapWindow(ports.WindowUnmapped{ID: 2})
		gone(t, c, sc, 2)
	})
	t.Run("other workspace", func(t *testing.T) {
		c, _, _, sc := twoSettled(t)
		sc.mon.Focus(1)
		indicatorScene(t, c)
		c.stopAnimations()
		c.unmapWindow(ports.WindowUnmapped{ID: 2})
		gone(t, c, sc, 2)
	})
	t.Run("session lock", func(t *testing.T) {
		c, _, _, sc := twoSettled(t)
		c.security = ports.SecurityState{Generation: 1, Protected: true}
		c.unmapWindow(ports.WindowUnmapped{ID: 2})
		if len(sc.rects) != 0 || c.animating() {
			t.Fatalf("an unmap under a session lock animated: %+v", sc.rects)
		}
		if _, w := c.screenOf(2); w != nil {
			t.Fatal("window 2 still placed")
		}
	})
	t.Run("overview open", func(t *testing.T) {
		c, _, _, sc := twoSettled(t)
		sc.mon.ToggleOverview()
		indicatorScene(t, c)
		c.unmapWindow(ports.WindowUnmapped{ID: 2})
		gone(t, c, sc, 2)
	})
	t.Run("never placed", func(t *testing.T) {
		c, _, _, sc := twoSettled(t)
		c.unmapWindow(ports.WindowUnmapped{ID: 9})
		if len(sc.rects) != 0 {
			t.Fatalf("an unknown window animated: %+v", sc.rects)
		}
	})
}

// A session lock during the fade drops the leaving entry with the other
// motions (stopRects), as do animations turned off.
func TestUnmapLeavingDroppedByLock(t *testing.T) {
	c, _, _, sc := twoSettled(t)
	c.unmapWindow(ports.WindowUnmapped{ID: 2})
	if s := indicatorScene(t, c); !slices.ContainsFunc(s.Windows, func(w ports.SceneWindow) bool { return w.ID == 2 }) {
		t.Fatal("no leaving entry")
	}
	c.stopAnimations()
	s := indicatorScene(t, c)
	if slices.ContainsFunc(s.Windows, func(w ports.SceneWindow) bool { return w.ID == 2 }) || len(sc.rects) != 0 || len(sc.shown) != 1 {
		t.Fatalf("after the stop: %+v", s.Windows)
	}
}
