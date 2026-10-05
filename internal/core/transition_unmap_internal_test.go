package core

import (
	"math"
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
	// The destroyed window got no configure at any point, and its last one
	// is forgotten once it left the scene.
	if ids := drainConfigures(cmds); slices.Contains(ids, 2) {
		t.Fatalf("the destroyed window was configured: %v", ids)
	}
	if _, ok := c.configures.sent[2]; ok {
		t.Fatal("configures still remember the destroyed window")
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

// A window closed while its own rect motion runs (its entrance here)
// leaves from what was drawn: the first leaving frame shows the rect and
// fade it had, not its settled rect, and the leaving motion replaces the
// running one.
func TestUnmapMidMotionLeavesFromDrawn(t *testing.T) {
	c, ic, _, sc := twoSettled(t)
	c.mapWindow(ports.WindowMapped{ID: 3})
	indicatorScene(t, c)
	ic.now = ic.now.Add(30 * time.Millisecond)
	c.animate(ic.now, nil)
	before := sceneWindow(t, indicatorScene(t, c), 3)
	settled3 := settledOf(t, sc, 3)
	if before.Rect == settled3.Rect || !(before.Fade > 0 && before.Fade < 1) {
		t.Fatalf("window 3 is not in flight (%+v): the case checks nothing", before)
	}
	c.unmapWindow(ports.WindowUnmapped{ID: 3})
	s := indicatorScene(t, c)
	w := sceneWindow(t, s, 3)
	if w.Rect != before.Rect || math.Abs(w.Fade-before.Fade) > 1e-9 {
		t.Fatalf("leaving window right after the unmap: %+v, want drawn as before %+v", w, before)
	}
	if rm, ok := sc.rects[3]; !ok || !rm.leaving {
		t.Fatalf("no leaving motion for window 3: %+v", rm)
	}
	ic.now = ic.now.Add(5 * time.Second)
	c.animate(ic.now, nil)
	if s := indicatorScene(t, c); slices.ContainsFunc(s.Windows, func(w ports.SceneWindow) bool { return w.ID == 3 }) || len(sc.rects) != 0 {
		t.Fatalf("the closed window is still there: %+v", s.Windows)
	}
}

// A tile closed while the stash hides: the absent leaving tile goes by
// withLeaving's two-pass rule (absent tiles by ID before the floats, the
// hidden stash windows replaced in place), the same on every refresh.
func TestUnmapWhileStashHidesOrdersDeterministically(t *testing.T) {
	c, ic, sc, cmds := stashRig(t, true)
	sc.mon.AddWindow(5)
	sc.mon.AddWindow(6)
	settleShown(t, c)
	drainConfigures(cmds)
	act(c, ic, ActionToggleStashVisible) // 2 and 3 hide, leaving in place
	c.unmapWindow(ports.WindowUnmapped{ID: 5})
	var want []WindowID
	for i := range 20 {
		s := indicatorScene(t, c)
		var ids []WindowID
		for _, w := range s.Windows {
			ids = append(ids, w.ID)
		}
		if i == 0 {
			want = ids
			k := slices.Index(sc.settledLayout, settledOf(t, sc, 5))
			if k < 0 || !sc.settledLayout[k].Leaving || !sc.shown[k].Leaving {
				t.Fatalf("the closed tile is not a leaving entry: %+v", sc.settledLayout)
			}
			for _, id := range []WindowID{2, 3} {
				if j := slices.Index(ids, id); j < 0 || !sc.settledLayout[j].Leaving {
					t.Fatalf("stash window %d not replaced in place: %v", id, ids)
				}
			}
			// The absent tile sits before any drawn float (the leaving
			// stash windows); a hidden, undrawn one may precede it.
			i5 := slices.Index(ids, 5)
			for j, w := range s.Windows {
				if w.Floating && !w.Hidden && j < i5 {
					t.Fatalf("order %v: float %d before the leaving tile 5", ids, w.ID)
				}
			}
			continue
		}
		if !slices.Equal(ids, want) {
			t.Fatalf("publish %d: order %v, first %v", i, ids, want)
		}
	}
	if ids := drainConfigures(cmds); slices.Contains(ids, 5) {
		t.Fatalf("the closed window was configured: %v", ids)
	}
	ic.now = ic.now.Add(5 * time.Second)
	c.animate(ic.now, nil)
	s := indicatorScene(t, c)
	if slices.ContainsFunc(s.Windows, func(w ports.SceneWindow) bool { return w.ID == 5 }) || len(sc.rects) != 0 {
		t.Fatalf("after the settle: %+v", s.Windows)
	}
	if ids := drainConfigures(cmds); slices.Contains(ids, 5) {
		t.Fatalf("the destroyed window was configured after the fade: %v", ids)
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
