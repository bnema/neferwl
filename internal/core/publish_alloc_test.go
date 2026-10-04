package core

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// Allocation budgets of one animation frame (step) on two screens of six
// windows each, with camera and rect motions running, and on one screen with
// the overview open. Every scene sent is freshly built (immutable once sent),
// and the layouts are rebuilt for every publish, so the floor is not 0.
// Measured: 43 before presizing the scene windows and separators, 36 after;
// 70 with the overview open (its rows and card layouts are rebuilt too).
const (
	publishAllocBudget         = 36
	publishOverviewAllocBudget = 70
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
	c, err := New(cfg, Channels{Scenes: make(chan []ports.Scene, 1), Commands: make(chan ports.ClientCommand, 64)})
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

// publishStep is one animation frame whose scene is taken from the channel.
func publishStep(t *testing.T, c *Core) func() {
	ctx := context.Background()
	return func() {
		if err := c.step(ctx, nil); err != nil {
			t.Fatal(err)
		}
		select {
		case <-c.ch.Scenes:
		default:
			t.Fatal("no scene published")
		}
	}
}

// TestPublishAllocations pins the cost of publishing an animation frame.
func TestPublishAllocations(t *testing.T) {
	c := publishRig(t, 2)
	t0 := time.Now()
	for _, sc := range c.screens {
		if sc.name() == "" {
			continue
		}
		ws := sc.mon.Current()
		ws.motion = c.spring(viewSpring(100, 0), t0)
		ws.shift = 100
		sc.rects = make(map[WindowID]rectMotion, 6)
		sp := viewSpring(100, 0)
		for _, p := range sc.settledLayout {
			sc.rects[p.ID] = rectMotion{
				x: c.spring(sp, t0), y: c.spring(sp, t0),
				w: c.spring(sp, t0), h: c.spring(sp, t0),
				dx: 100, dy: 100, dw: 100, dh: 100,
			}
		}
		sc.rectsWS = ws
	}
	step := publishStep(t, c)
	step()
	for _, sc := range c.screens {
		if sc.name() == "" {
			continue
		}
		if len(sc.rects) != 6 || !sc.mon.Current().motion.on || len(sc.shown) < 6 {
			t.Fatalf("setup: rects %d camera %v shown %d", len(sc.rects), sc.mon.Current().motion.on, len(sc.shown))
		}
	}
	n := testing.AllocsPerRun(50, step)
	t.Logf("allocs per step = %v", n)
	for _, sc := range c.screens {
		if sc.name() == "" {
			continue
		}
		if len(sc.rects) != 6 || !sc.mon.Current().motion.on {
			t.Fatalf("motions settled during the measure: rects %d", len(sc.rects))
		}
	}
	if n > publishAllocBudget {
		t.Errorf("publish allocs per frame = %v, budget %d", n, publishAllocBudget)
	}
}

// TestPublishOverviewAllocations pins the cost of publishing with the
// overview open, which guards the reuse of Core.overviewReal.
func TestPublishOverviewAllocations(t *testing.T) {
	c := publishRig(t, 1)
	sc := c.cur()
	sc.mon.ToggleOverview()
	step := publishStep(t, c)
	step()
	if !sc.mon.ov.open || len(c.overviewReal) != 6 {
		t.Fatalf("setup: overview %v, real layouts %d", sc.mon.ov.open, len(c.overviewReal))
	}
	n := testing.AllocsPerRun(50, step)
	t.Logf("overview allocs per step = %v", n)
	if n > publishOverviewAllocBudget {
		t.Errorf("overview publish allocs per frame = %v, budget %d", n, publishOverviewAllocBudget)
	}
}
