package core

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// Allocation budgets of one animation frame (step) on two screens of six
// windows each, with camera and rect motions (position, fade, dim, scale)
// running, and on one screen with the overview opening (its cards' scale
// and fade motions running). Every scene sent is freshly built (immutable
// once sent), so the floor is not 0. Measured: 43 before presizing the scene
// windows and separators, 36 after, 32 once the fallback timer is made once
// and reset per frame (the fallback path, step(ctx, nil), re-arms it; the
// flip path with another screen animating keeps it and costs the same), then
// 6 once the layouts are built in the owner's reused buffers (layoutInto:
// the screens' layout, the workspaces' column, row and tile rects) instead of
// fresh slices. What is left is the scenes themselves (windows, separators,
// the scene set) and the screen placements. Leaving entries (a closed window
// still fading) add none: 4, less than a frame with six moving windows
// (fewer configure targets). The opening overview costs 18 (the stack
// items, the row map and the neighbor list it rebuilds every frame; its
// layouts reuse the buffers too).
const (
	publishAllocBudget         = 6
	publishLeavingAllocBudget  = 4
	publishOverviewAllocBudget = 18
)

// publishRig is a Core on the real clock (a mock clock would count its own
// bookkeeping; the fallback timer is then the system one) with screens
// screens of six windows each. The Slowdown cap keeps every motion running
// for seconds, so none settles during a measure even under -race or load.
func publishRig(t *testing.T, screens int) *Core {
	t.Helper()
	var cfg ports.Config
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 2
	cfg.Border.Width = 2
	cfg.Animations.On = true
	cfg.Animations.Slowdown = 10
	c, err := New(cfg, Channels{Scenes: make(chan []ports.Scene, 1), Commands: make(chan ports.ClientCommand, 64)}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	next := WindowID(1)
	for i := range screens {
		c.addScreen(ports.OutputInfo{Name: string(rune('A' + i)), Width: 300, Height: 200})
	}
	for _, sc := range c.screens {
		if sc.name() == "" {
			continue
		}
		for range 6 {
			sc.mon.AddWindow(next)
			next++
		}
		sc.arrange()
	}
	c.refreshShown()
	return c
}

// publishStep is one animation frame of only (nil: the fallback timer's,
// every screen) whose scene is taken from the channel.
func publishStep(t *testing.T, c *Core, only *screen) func() {
	ctx := context.Background()
	return func() {
		if err := c.step(ctx, only); err != nil {
			t.Fatal(err)
		}
		select {
		case <-c.ch.Scenes:
		default:
			t.Fatal("no scene published")
		}
	}
}

// startRectMotions gives every window of sc a rect motion on each
// component: position and size, fade, dim and content scale.
func startRectMotions(c *Core, sc *screen, t0 time.Time) {
	sc.rects = make(map[WindowID]rectMotion, 6)
	sp := viewSpring(100, 0)
	for _, p := range sc.settledLayout {
		sc.rects[p.ID] = rectMotion{
			x: c.spring(sp, t0), y: c.spring(sp, t0),
			w: c.spring(sp, t0), h: c.spring(sp, t0),
			dx: 100, dy: 100, dw: 100, dh: 100,
			fade: c.spring(levelSpring(0.5, 0), t0), df: 0.5,
			dim: c.spring(levelSpring(0.5, 0), t0), ddim: 0.5,
			scale: true,
		}
	}
	sc.rectsWS = sc.mon.Current()
}

// checkRunning fails unless every real screen still has six rect motions
// and its camera motion.
func checkRunning(t *testing.T, c *Core, when string) {
	t.Helper()
	for _, sc := range c.screens {
		if sc.name() == "" {
			continue
		}
		if len(sc.rects) != 6 || !sc.mon.Current().view.motion.on || len(sc.shown) < 6 {
			t.Fatalf("%s: rects %d camera %v shown %d", when, len(sc.rects), sc.mon.Current().view.motion.on, len(sc.shown))
		}
	}
}

// TestPublishAllocations pins the cost of publishing an animation frame, on
// the fallback timer (every screen) and on a page flip of one screen while
// the other still animates (the flip path keeps the timer).
func TestPublishAllocations(t *testing.T) {
	c := publishRig(t, 2)
	t0 := time.Now()
	for _, sc := range c.screens {
		if sc.name() == "" {
			continue
		}
		ws := sc.mon.Current()
		ws.view.motion = c.spring(viewSpring(100, 0), t0)
		ws.view.off = 100
		startRectMotions(c, sc, t0)
	}
	for _, tc := range []struct {
		name string
		only *screen
	}{{"fallback", nil}, {"flip", c.screens[1]}} {
		step := publishStep(t, c, tc.only)
		step()
		checkRunning(t, c, "setup")
		if tc.only != nil && !c.animatingOther(tc.only) {
			t.Fatal("setup: the other screen does not animate")
		}
		n := testing.AllocsPerRun(50, step)
		t.Logf("%s allocs per step = %v", tc.name, n)
		checkRunning(t, c, "motions settled during the measure")
		if n > publishAllocBudget {
			t.Errorf("%s publish allocs per frame = %v, budget %d", tc.name, n, publishAllocBudget)
		}
	}
}

// TestPublishStashSlideAllocations pins the cost of publishing a frame while
// the stash slides: windows laid out at a fractional offset, each with a
// partial veil, with the same budget as any other animation frame.
func TestPublishStashSlideAllocations(t *testing.T) {
	c := publishRig(t, 1)
	sc := c.screens[0]
	ws := sc.mon.Current()
	for _, id := range []WindowID{2, 3, 4, 5} {
		ws.FocusID(id)
		ws.ToggleWindowStash()
	}
	sc.arrange()
	c.refreshShown()
	ws.stashView.off = 0.5
	ws.stashView.motion = c.spring(ws.stashSpring(0.5, 0), time.Now())
	step := publishStep(t, c, nil)
	step()
	if !ws.stashView.motion.on || !ws.stashShown() {
		t.Fatal("setup: no stash slide on screen")
	}
	var partial int
	for _, p := range sc.shown {
		if p.Veil > 0 && p.Veil < 1 {
			partial++
		}
	}
	if partial == 0 {
		t.Fatal("setup: no window draws a partial veil")
	}
	n := testing.AllocsPerRun(50, step)
	t.Logf("stash slide allocs per step = %v", n)
	if !ws.stashView.motion.on {
		t.Fatal("the slide settled during the measure")
	}
	if n > publishAllocBudget {
		t.Errorf("stash slide publish allocs per frame = %v, budget %d", n, publishAllocBudget)
	}
}

// TestPublishOverviewAllocations pins the cost of publishing while the
// overview opens: its cards fly from the full rects (scale and fade motions
// on every card), which guards the reuse of Core.overviewReal.
func TestPublishOverviewAllocations(t *testing.T) {
	c := publishRig(t, 1)
	sc := c.cur()
	now := time.Now()
	shots := c.snapshot(now)
	sc.mon.ToggleOverview()
	c.transition(shots, now)
	step := publishStep(t, c, nil)
	step()
	if !sc.mon.ov.open || len(c.overviewReal) != 6 || len(sc.rects) == 0 || !c.animating() {
		t.Fatalf("setup: overview %v, real layouts %d, rects %d, animating %v", sc.mon.ov.open, len(c.overviewReal), len(sc.rects), c.animating())
	}
	n := testing.AllocsPerRun(50, step)
	t.Logf("overview allocs per step = %v", n)
	if len(sc.rects) == 0 {
		t.Fatal("card motions settled during the measure")
	}
	if n > publishOverviewAllocBudget {
		t.Errorf("overview publish allocs per frame = %v, budget %d", n, publishOverviewAllocBudget)
	}
}

// TestPublishLeavingAllocations pins the cost of a frame with leaving
// entries on every screen (two of the six windows closed, fading out, the
// other four with rect motions): the measured 4, under a frame without
// them (the leaving entries take no configure target).
func TestPublishLeavingAllocations(t *testing.T) {
	c := publishRig(t, 2)
	t0 := time.Now()
	for _, sc := range c.screens {
		if sc.name() == "" {
			continue
		}
		c.refreshShown()
		layout := sc.settledLayout
		sc.rects = make(map[WindowID]rectMotion, 6)
		sp := viewSpring(100, 0)
		for i, p := range layout {
			if i < 2 {
				sc.mon.RemoveWindow(p.ID)
				c.leaveFrom(sc, p, nil, t0)
				continue
			}
			sc.rects[p.ID] = rectMotion{
				x: c.spring(sp, t0), y: c.spring(sp, t0),
				w: c.spring(sp, t0), h: c.spring(sp, t0),
				dx: 100, dy: 100, dw: 100, dh: 100,
			}
		}
		sc.rectsWS = sc.mon.Current()
	}
	step := publishStep(t, c, nil)
	step()
	for _, sc := range c.screens {
		if sc.name() == "" {
			continue
		}
		leaving := 0
		for _, p := range sc.shown {
			if p.Leaving {
				leaving++
			}
		}
		if len(sc.rects) != 6 || leaving != 2 || len(sc.shown) != 6 {
			t.Fatalf("setup: rects %d leaving %d shown %d", len(sc.rects), leaving, len(sc.shown))
		}
	}
	n := testing.AllocsPerRun(50, step)
	t.Logf("leaving allocs per step = %v", n)
	for _, sc := range c.screens {
		if sc.name() != "" && len(sc.rects) != 6 {
			t.Fatalf("motions settled during the measure: rects %d", len(sc.rects))
		}
	}
	if n > publishLeavingAllocBudget {
		t.Errorf("publish allocs per frame with leaving entries = %v, budget %d", n, publishLeavingAllocBudget)
	}
}
