package core

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// A tiled map: the new window fades in from 90 % of its settled rect, its
// neighbour slides from where it was, the client is configured once, to
// the final size, and everything settles exactly.
func TestMapTiledAppearsAndReflows(t *testing.T) {
	c, ic, cmds := fadeCore(t)
	c.cfg.Animations.On = true
	sc := c.cur()
	c.mapWindow(ports.WindowMapped{ID: 1})
	indicatorScene(t, c)
	c.animate(ic.now.Add(5*time.Second), nil)
	ic.now = ic.now.Add(5 * time.Second)
	indicatorScene(t, c)
	if len(sc.rects) != 0 {
		t.Fatalf("motions left after the first window settled: %d", len(sc.rects))
	}
	drainConfigures(cmds)
	old1 := sceneWindow(t, indicatorScene(t, c), 1).Rect

	c.mapWindow(ports.WindowMapped{ID: 2})
	s := indicatorScene(t, c)
	settled2 := settledOf(t, sc, 2)
	w2 := sceneWindow(t, s, 2)
	if w2.Fade != 1 || w2.Hidden || w2.Rect != scaledRect(settled2.Rect, appearScale) || math.Abs(w2.Zoom-appearScale) > 0.02 || w2.Preview != 0 {
		t.Fatalf("new window right after the map: %+v, settled %+v", w2, settled2.Rect)
	}
	// The neighbour is drawn where it was, and moves to its new half.
	if got := sceneWindow(t, s, 1).Rect; got != old1 {
		t.Fatalf("neighbour right after the map at %+v, want the old %+v", got, old1)
	}
	settled1 := settledOf(t, sc, 1)
	if settled1.Rect == old1 {
		t.Fatal("the new column did not move the neighbour: the case checks nothing")
	}
	// One configure per window, to the settled size.
	got := drainConfigures(cmds)
	slices.Sort(got)
	if !slices.Equal(got, []WindowID{1, 2}) {
		t.Fatalf("configures %v, want one each for 1 and 2", got)
	}

	ic.now = ic.now.Add(40 * time.Millisecond)
	c.animate(ic.now, nil)
	s = indicatorScene(t, c)
	w2 = sceneWindow(t, s, 2)
	if !(w2.Fade > 0 && w2.Fade < 1) || !(w2.Zoom > appearScale && w2.Zoom < 1) || !(w2.Rect.W > scaledRect(settled2.Rect, appearScale).W && w2.Rect.W < settled2.Rect.W) {
		t.Fatalf("first frame of the new window: %+v", w2)
	}
	if r := sceneWindow(t, s, 1).Rect; !(r.W < old1.W && r.W > settled1.Rect.W) {
		t.Fatalf("neighbour on the first frame %+v, want between %+v and %+v", r, old1, settled1.Rect)
	}
	if ids := drainConfigures(cmds); len(ids) != 0 {
		t.Fatalf("frames reconfigured %v", ids)
	}

	ic.now = ic.now.Add(5 * time.Second)
	c.animate(ic.now, nil)
	s = indicatorScene(t, c)
	if w := sceneWindow(t, s, 2); w.Fade != 0 || w.Zoom != 0 || w.Rect != settled2.Rect {
		t.Fatalf("new window settled: %+v, want %+v", w, settled2.Rect)
	}
	if w := sceneWindow(t, s, 1); w.Rect != settled1.Rect {
		t.Fatalf("neighbour settled at %+v, want %+v", w.Rect, settled1.Rect)
	}
	if len(sc.rects) != 0 || c.animating() {
		t.Fatal("still animating after the settle")
	}
}

// A floating toplevel appears too.
func TestMapFloatingAppears(t *testing.T) {
	c, ic, _ := fadeCore(t)
	c.cfg.Animations.On = true
	sc := c.cur()
	c.mapWindow(ports.WindowMapped{ID: 1})
	indicatorScene(t, c)
	c.mapWindow(ports.WindowMapped{ID: 2, Floating: true, Width: 80, Height: 60})
	s := indicatorScene(t, c)
	settled := settledOf(t, sc, 2)
	w := sceneWindow(t, s, 2)
	if !w.Floating && !settled.Floating {
		t.Fatalf("window 2 is not a float: %+v", settled)
	}
	if w.Fade != 1 || w.Rect != scaledRect(settled.Rect, appearScale) {
		t.Fatalf("float right after the map: %+v, settled %+v", w, settled.Rect)
	}
	ic.now = ic.now.Add(5 * time.Second)
	c.animate(ic.now, nil)
	if w := sceneWindow(t, indicatorScene(t, c), 2); w.Fade != 0 || w.Rect != settled.Rect {
		t.Fatalf("float settled: %+v", w)
	}
}

// With animations off a map is instant: no motion, no fade.
func TestMapOffIsInstant(t *testing.T) {
	c, _, _ := fadeCore(t)
	sc := c.cur()
	c.mapWindow(ports.WindowMapped{ID: 1})
	indicatorScene(t, c)
	c.mapWindow(ports.WindowMapped{ID: 2})
	s := indicatorScene(t, c)
	if len(sc.rects) != 0 || c.animating() {
		t.Fatal("a map started a motion with animations off")
	}
	for _, w := range s.Windows {
		if w.Fade != 0 || w.Zoom != 0 || w.Rect != settledOf(t, sc, w.ID).Rect {
			t.Fatalf("window %d not at its settled rect: %+v", w.ID, w)
		}
	}
}

// A window that is not shown tiled or floating on screen does not appear:
// fullscreen, hidden behind a fullscreen game, with a session lock or with
// the overview open. Neighbours stay put as well in the last two.
func TestMapExceptions(t *testing.T) {
	t.Run("fullscreen", func(t *testing.T) {
		c, ic, _ := fadeCore(t)
		c.cfg.Animations.On = true
		sc := c.cur()
		c.mapWindow(ports.WindowMapped{ID: 1})
		c.stopAnimations() // the first window's own entrance
		sc.mon.SetFullscreen(1, true)
		c.appearMapped(1, ic.now)
		indicatorScene(t, c)
		if len(sc.rects) != 0 || c.animating() {
			t.Fatalf("a fullscreen window appeared: %+v", sc.rects)
		}
	})
	t.Run("behind a fullscreen game", func(t *testing.T) {
		c, _, _ := fadeCore(t)
		c.cfg.Animations.On = true
		sc := c.cur()
		sc.mon.Current().Overflow = OverflowFixed
		c.mapWindow(ports.WindowMapped{ID: 1})
		c.stopAnimations() // the first window's own entrance
		sc.mon.SetFullscreen(1, true)
		indicatorScene(t, c)
		c.mapWindow(ports.WindowMapped{ID: 2})
		if s := indicatorScene(t, c); len(sc.rects) != 0 || sceneWindow(t, s, 2).Fade != 0 {
			t.Fatalf("a window mapped behind a fullscreen one appeared: %+v", sc.rects)
		}
	})
	t.Run("session lock", func(t *testing.T) {
		c, _, _ := fadeCore(t)
		c.cfg.Animations.On = true
		sc := c.cur()
		c.mapWindow(ports.WindowMapped{ID: 1})
		c.stopAnimations() // the first window's own entrance
		c.security = ports.SecurityState{Generation: 1, Protected: true}
		c.mapWindow(ports.WindowMapped{ID: 2})
		if len(sc.rects) != 0 || c.animating() {
			t.Fatalf("a window mapped under a session lock animated: %+v", sc.rects)
		}
	})
	t.Run("overview open", func(t *testing.T) {
		c, _, _ := fadeCore(t)
		c.cfg.Animations.On = true
		sc := c.cur()
		c.mapWindow(ports.WindowMapped{ID: 1})
		c.stopAnimations() // the first window's own entrance
		sc.mon.ToggleOverview()
		c.mapWindow(ports.WindowMapped{ID: 2})
		if len(sc.rects) != 0 {
			t.Fatalf("a window mapped in the overview animated: %+v", sc.rects)
		}
	})
	t.Run("map again", func(t *testing.T) {
		c, ic, _ := fadeCore(t)
		c.cfg.Animations.On = true
		sc := c.cur()
		c.mapWindow(ports.WindowMapped{ID: 1})
		c.stopAnimations() // the first window's own entrance
		indicatorScene(t, c)
		ic.now = ic.now.Add(5 * time.Second)
		c.mapWindow(ports.WindowMapped{ID: 1})
		c.stopAnimations() // the first window's own entrance
		if len(sc.rects) != 0 {
			t.Fatalf("a repeated map restarted the entrance: %+v", sc.rects)
		}
	})
}

// A float maps before its first commit: Width and Height are 0, so core lays
// it out at a default size, and the real one comes in WindowResized. The
// entrance must restart from 90 % of the real size, not stay distorted.
func TestMapFloatEntranceFollowsItsFirstCommit(t *testing.T) {
	c, ic, _ := fadeCore(t)
	c.cfg.Animations.On = true
	sc := c.cur()
	c.mapWindow(ports.WindowMapped{ID: 1})
	c.stopAnimations()
	c.mapWindow(ports.WindowMapped{ID: 2, Floating: true})
	indicatorScene(t, c)
	guess := settledOf(t, sc, 2).Rect

	c.resizeFloating(ports.WindowResized{ID: 2, Width: 60, Height: 20})
	s := indicatorScene(t, c)
	real := settledOf(t, sc, 2).Rect
	if real == guess || real.W == real.H*guess.W/guess.H {
		t.Fatalf("the resize did not change the settled shape: %+v then %+v", guess, real)
	}
	w := sceneWindow(t, s, 2)
	want := scaledRect(real, appearScale)
	near := func(a, b int) bool { return a-b <= 1 && b-a <= 1 }
	if w.Fade != 1 || !near(w.Rect.X, want.X) || !near(w.Rect.Y, want.Y) || !near(w.Rect.W, want.W) || !near(w.Rect.H, want.H) {
		t.Fatalf("first frame %+v, want 90 %% of the real %+v = %+v", w, real, want)
	}
	if math.Abs(w.Zoom-appearScale) > 0.02 {
		t.Fatalf("zoom %v, want ~0.9 (aspect kept)", w.Zoom)
	}

	// Mid-way the frame keeps the real aspect, the zoom follows its width.
	ic.now = ic.now.Add(40 * time.Millisecond)
	c.animate(ic.now, nil)
	w = sceneWindow(t, indicatorScene(t, c), 2)
	ratio, wantRatio := float64(w.Rect.W)/float64(w.Rect.H), float64(real.W)/float64(real.H)
	if math.Abs(ratio-wantRatio)/wantRatio > 0.06 || !(w.Rect.W > want.W && w.Rect.W < real.W) || math.Abs(w.Zoom-float64(w.Rect.W)/float64(real.W)) > 0.01 {
		t.Fatalf("mid-way %+v: aspect %.3f want %.3f", w, ratio, wantRatio)
	}

	ic.now = ic.now.Add(5 * time.Second)
	c.animate(ic.now, nil)
	if w := sceneWindow(t, indicatorScene(t, c), 2); w.Fade != 0 || w.Zoom != 0 || w.Rect != real {
		t.Fatalf("settled %+v, want %+v", w, real)
	}
}

// A resize with no entrance running only moves the settled rect.
func TestResizeFloatWithoutEntranceKeepsNoMotion(t *testing.T) {
	c, _, _ := fadeCore(t)
	c.cfg.Animations.On = true
	sc := c.cur()
	c.mapWindow(ports.WindowMapped{ID: 1, Floating: true})
	c.stopAnimations()
	c.resizeFloating(ports.WindowResized{ID: 1, Width: 60, Height: 20})
	if len(sc.rects) != 0 {
		t.Fatalf("a resize started a motion: %+v", sc.rects)
	}
}
