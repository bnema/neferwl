package core

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// fadeCore is indicatorCore with its commands readable.
func fadeCore(t *testing.T) (*Core, *indicatorClock, chan ports.ClientCommand) {
	t.Helper()
	c, ic := indicatorCore(t)
	cmds := make(chan ports.ClientCommand, 64)
	c.ch.Commands = cmds
	return c, ic, cmds
}

// drainConfigures returns the IDs configured since the last drain.
func drainConfigures(cmds chan ports.ClientCommand) []WindowID {
	var ids []WindowID
	for {
		select {
		case cmd := <-cmds:
			if v, ok := cmd.(ports.ConfigureWindow); ok {
				ids = append(ids, v.ID)
			}
		default:
			return ids
		}
	}
}

func sceneWindow(t *testing.T, s ports.Scene, id WindowID) ports.SceneWindow {
	t.Helper()
	i := slices.IndexFunc(s.Windows, func(w ports.SceneWindow) bool { return w.ID == id })
	if i < 0 {
		t.Fatalf("window %d not in the scene: %+v", id, s.Windows)
	}
	return s.Windows[i]
}

// appear fades a window in from invisible and grows it from 90 % of its
// settled rect, its content zoomed to the drawn size; the shown placement
// carries the derived Fade and Preview, the settled one neither.
func TestAppearDerivesFadeAndPreview(t *testing.T) {
	c, ic, cmds := fadeCore(t)
	c.cfg.Animations.On = true
	sc := c.cur()
	sc.mon.AddWindow(1)
	sc.mon.AddWindow(2)
	settleShown(t, c)
	drainConfigures(cmds)
	settled := sc.settledLayout[slices.IndexFunc(sc.settledLayout, func(p Placement) bool { return p.ID == 2 })]

	c.appear(sc, 2, ic.now)
	s := indicatorScene(t, c)
	w := sceneWindow(t, s, 2)
	if w.Fade != 1 || w.Hidden {
		t.Fatalf("right after appear: fade %v hidden %v, want invisible and shown", w.Fade, w.Hidden)
	}
	want := scaledRect(settled.Rect, appearScale)
	if w.Rect != want {
		t.Fatalf("first rect %+v, want 90 %% of %+v = %+v", w.Rect, settled.Rect, want)
	}
	if math.Abs(w.Preview-0.9) > 0.02 {
		t.Fatalf("preview %v, want ~0.9 (content follows the drawn size)", w.Preview)
	}
	if ps := sc.settledLayout[slices.IndexFunc(sc.settledLayout, func(p Placement) bool { return p.ID == 2 })]; ps != settled {
		t.Fatalf("settled placement changed: %+v vs %+v", ps, settled)
	}
	if ids := drainConfigures(cmds); len(ids) != 0 {
		t.Fatalf("appear reconfigured %v", ids)
	}
	// The derived zoom is not a card: the window takes input and keeps
	// its popups while it appears.
	if id, _, _ := c.hit(float64(w.Rect.X+w.Rect.W/2), float64(w.Rect.Y+w.Rect.H/2)); id != 2 {
		t.Fatalf("hit %d while appearing, want 2", id)
	}
	if _, _, ok := c.windowRect(2); !ok {
		t.Fatal("windowRect hides the appearing window")
	}

	ic.now = ic.now.Add(40 * time.Millisecond)
	c.animate(ic.now, nil)
	s = indicatorScene(t, c)
	w = sceneWindow(t, s, 2)
	if !(w.Fade > 0 && w.Fade < 1) || !(w.Preview > 0.9 && w.Preview < 1) {
		t.Fatalf("mid-way fade %v preview %v, want strictly between", w.Fade, w.Preview)
	}
	if !(w.Rect.W > want.W && w.Rect.W < settled.Rect.W) {
		t.Fatalf("mid-way width %d, want between %d and %d", w.Rect.W, want.W, settled.Rect.W)
	}

	ic.now = ic.now.Add(5 * time.Second)
	c.animate(ic.now, nil)
	s = indicatorScene(t, c)
	w = sceneWindow(t, s, 2)
	if w.Fade != 0 || w.Preview != 0 || w.Rect != settled.Rect || len(sc.rects) != 0 {
		t.Fatalf("settled: %+v, rects %d", w, len(sc.rects))
	}
	if c.animating() {
		t.Fatal("still animating after the settle")
	}
}

// With animations off appear does nothing.
func TestAppearOffIsInstant(t *testing.T) {
	c, ic := indicatorCore(t)
	sc := c.cur()
	sc.mon.AddWindow(1)
	settleShown(t, c)
	c.appear(sc, 1, ic.now)
	if len(sc.rects) != 0 || c.animating() {
		t.Fatal("appear started a motion with animations off")
	}
	if w := sceneWindow(t, indicatorScene(t, c), 1); w.Fade != 0 || w.Preview != 0 {
		t.Fatalf("faded with animations off: %+v", w)
	}
}

// A leaving window stays in the scene while it fades out and shrinks, is
// hidden to input, focus and configures, keeps settled and shown aligned,
// and goes once settled.
func TestLeavingEntryLifecycle(t *testing.T) {
	c, ic, cmds := fadeCore(t)
	c.cfg.Animations.On = true
	sc := c.cur()
	sc.mon.AddWindow(1)
	sc.mon.AddWindow(2)
	settleShown(t, c)
	drainConfigures(cmds)
	left := sc.settledLayout[slices.IndexFunc(sc.settledLayout, func(p Placement) bool { return p.ID == 2 })]

	// Close window 2: the layout re-flows; its last placement leaves.
	sc.mon.RemoveWindow(2)
	c.leave(sc, left, ic.now)
	s := indicatorScene(t, c)
	if len(sc.settledLayout) != len(sc.shown) {
		t.Fatalf("settled %d and shown %d entries", len(sc.settledLayout), len(sc.shown))
	}
	for k := range sc.shown {
		if sc.shown[k].ID != sc.settledLayout[k].ID {
			t.Fatalf("index %d: shown %d, settled %d", k, sc.shown[k].ID, sc.settledLayout[k].ID)
		}
	}
	k := slices.IndexFunc(sc.shown, func(p Placement) bool { return p.ID == 2 })
	if k < 0 || !sc.shown[k].Leaving || !sc.shown[k].Hidden || !sc.settledLayout[k].Hidden {
		t.Fatalf("leaving entry: shown %+v settled %+v", sc.shown[k], sc.settledLayout[k])
	}
	w := sceneWindow(t, s, 2)
	if w.Hidden || w.Fade != 0 || w.Rect != left.Rect || w.Preview != 0 {
		t.Fatalf("first leaving frame %+v, want drawn opaque at %+v", w, left.Rect)
	}
	if ids := drainConfigures(cmds); slices.Contains(ids, 2) {
		t.Fatalf("the leaving window was configured: %v", ids)
	}
	// Window 1 took the whole frame: the leaving one over it is not hit.
	cx, cy := float64(left.Rect.X+left.Rect.W/2), float64(left.Rect.Y+left.Rect.H/2)
	if id, _, _ := c.hit(cx, cy); id != 1 {
		t.Fatalf("hit %d at the leaving window's centre, want 1", id)
	}
	if id, _ := sc.mon.Focused(); id == 2 {
		t.Fatal("the leaving window is focused")
	}
	if _, _, ok := c.windowRect(2); ok {
		t.Fatal("windowRect reports the leaving window on screen")
	}
	if !c.animating() {
		t.Fatal("no motion runs for the leaving window")
	}

	ic.now = ic.now.Add(40 * time.Millisecond)
	c.animate(ic.now, nil)
	s = indicatorScene(t, c)
	w = sceneWindow(t, s, 2)
	if !(w.Fade > 0 && w.Fade < 1) || !(w.Rect.W < left.Rect.W) || !(w.Preview > 0.9 && w.Preview < 1) {
		t.Fatalf("mid-way %+v", w)
	}

	ic.now = ic.now.Add(5 * time.Second)
	c.animate(ic.now, nil)
	s = indicatorScene(t, c)
	if slices.ContainsFunc(s.Windows, func(w ports.SceneWindow) bool { return w.ID == 2 }) {
		t.Fatalf("the leaving window is still in the scene: %+v", s.Windows)
	}
	if len(sc.rects) != 0 || c.animating() || len(sc.shown) != len(sc.settledLayout) {
		t.Fatalf("after the settle: rects %d animating %v", len(sc.rects), c.animating())
	}
}

// A leaving window the layout shows again (the stash reopened before the
// fade ended) drops its leaving entry at once: the layout's placement wins.
func TestLeavingEntryDroppedWhenShownAgain(t *testing.T) {
	c, ic := indicatorCore(t)
	c.cfg.Animations.On = true
	sc := c.cur()
	sc.mon.AddWindow(1)
	sc.mon.AddWindow(2)
	settleShown(t, c)
	left := sc.settledLayout[1]
	sc.mon.RemoveWindow(2)
	c.leave(sc, left, ic.now)
	if s := indicatorScene(t, c); len(s.Windows) != 2 || !sc.shown[1].Leaving {
		t.Fatalf("no leaving entry: %+v", s.Windows)
	}
	sc.mon.AddWindow(2)
	s := indicatorScene(t, c)
	n := 0
	for _, w := range s.Windows {
		if w.ID == 2 {
			n++
			if w.Fade != 0 || w.Preview != 0 {
				t.Fatalf("the re-added window keeps the leaving fade: %+v", w)
			}
		}
	}
	if n != 1 || len(sc.rects) != 0 || slices.ContainsFunc(sc.shown, func(p Placement) bool { return p.Leaving }) {
		t.Fatalf("window 2 listed %d times, rects %d: %+v", n, len(sc.rects), sc.shown)
	}
}

// stopRects (lock, off, overview, workspace change, unplug) drops leaving
// entries with the other motions.
func TestLeavingEntryDroppedByStopRects(t *testing.T) {
	c, ic := indicatorCore(t)
	c.cfg.Animations.On = true
	sc := c.cur()
	sc.mon.AddWindow(1)
	sc.mon.AddWindow(2)
	settleShown(t, c)
	left := sc.settledLayout[1]
	sc.mon.RemoveWindow(2)
	c.leave(sc, left, ic.now)
	if s := indicatorScene(t, c); len(s.Windows) != 2 {
		t.Fatalf("%d windows, want the tile and the leaving entry", len(s.Windows))
	}
	c.stopAnimations()
	s := indicatorScene(t, c)
	if len(s.Windows) != 1 || s.Windows[0].ID != 1 || len(sc.shown) != 1 {
		t.Fatalf("after stop: %+v", s.Windows)
	}
}

// With animations off a leaving window is simply gone.
func TestLeaveOffIsInstant(t *testing.T) {
	c, ic := indicatorCore(t)
	sc := c.cur()
	sc.mon.AddWindow(1)
	settleShown(t, c)
	left := sc.settledLayout[0]
	sc.mon.RemoveWindow(1)
	c.leave(sc, left, ic.now)
	if s := indicatorScene(t, c); len(s.Windows) != 0 || len(sc.rects) != 0 {
		t.Fatalf("leaving entry with animations off: %+v", s.Windows)
	}
}

// A dim motion animates the veil of a shown window from an offset to the
// settled Dim; a peeking window adds it to the configured stash dim.
func TestDimMotionAnimatesSceneDim(t *testing.T) {
	c, ic := indicatorCore(t)
	c.cfg.Animations.On = true
	sc := c.cur()
	sc.mon.AddWindow(1)
	settleShown(t, c)
	sc.rects = map[WindowID]rectMotion{}
	var rm rectMotion
	c.retargetComponent(&rm.dim, &rm.ddim, 0.5, 0, ic.now)
	sc.rects[1], sc.rectsWS = rm, sc.mon.Current()
	if w := sceneWindow(t, indicatorScene(t, c), 1); w.Dim != 0.5 {
		t.Fatalf("dim %v, want the 0.5 offset", w.Dim)
	}
	ic.now = ic.now.Add(40 * time.Millisecond)
	c.animate(ic.now, nil)
	if w := sceneWindow(t, indicatorScene(t, c), 1); !(w.Dim > 0 && w.Dim < 0.5) {
		t.Fatalf("mid-way dim %v", w.Dim)
	}
	ic.now = ic.now.Add(5 * time.Second)
	c.animate(ic.now, nil)
	if w := sceneWindow(t, indicatorScene(t, c), 1); w.Dim != 0 || len(sc.rects) != 0 {
		t.Fatalf("settled dim %v, rects %d", w.Dim, len(sc.rects))
	}
}

// A scale motion on an overview card multiplies the card's settled zoom
// into Zoom; Preview stays settled (input tells cards by it).
func TestScaleMotionMultipliesSettledPreview(t *testing.T) {
	var rm rectMotion
	rm.scale, rm.dw = true, 50
	p := Placement{ID: 1, Rect: Rect{W: 100, H: 100}, Preview: 0.2}
	rm.show(&p)
	if p.Rect.W != 150 || math.Abs(p.Zoom-0.3) > 1e-9 || p.Preview != 0.2 {
		t.Fatalf("shown %+v, want width 150, zoom 0.3 and the settled preview", p)
	}
	// A plain window drawn at its size: Zoom 1 (publish emits Preview 0).
	rm.dw = 0.01
	p = Placement{ID: 1, Rect: Rect{W: 100, H: 100}}
	rm.show(&p)
	if p.Zoom != 1 {
		t.Fatalf("zoom %v for an unscaled window, want 1", p.Zoom)
	}
}

// snapshot leaves leaving entries out (they are Hidden), so a transition
// after a close never moves them; the neighbours' rects are recorded.
func TestSnapshotSkipsLeavingEntries(t *testing.T) {
	c, ic := indicatorCore(t)
	c.cfg.Animations.On = true
	sc := c.cur()
	sc.mon.AddWindow(1)
	sc.mon.AddWindow(2)
	settleShown(t, c)
	left := sc.settledLayout[1]
	sc.mon.RemoveWindow(2)
	c.leave(sc, left, ic.now)
	settleShown(t, c)
	shots := c.snapshot(ic.now)
	if len(shots) != 1 || len(shots[0].rects) != 1 || shots[0].rects[0].id != 1 {
		t.Fatalf("snapshot rects %+v, want window 1 only", shots[0].rects)
	}
}
