package core

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// publishAllocBudget is the allocations of one animation frame (step) on two
// screens of six windows each, with camera and rect motions running. Every
// scene sent is freshly built (immutable once sent), and the layouts are
// rebuilt for every publish, so the floor is not 0. Measured: 43 before
// presizing the scene windows and separators, 36 after.
const publishAllocBudget = 36

// TestPublishAllocations pins the cost of publishing an animation frame.
func TestPublishAllocations(t *testing.T) {
	// No Clock port: a mock clock would count its own bookkeeping, and the
	// fallback timer then is the system one.
	var cfg ports.Config
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 2
	cfg.Border.Width = 2
	cfg.Animations.On = true
	c, err := New(cfg, Channels{Scenes: make(chan []ports.Scene, 1), Commands: make(chan ports.ClientCommand, 64)})
	if err != nil {
		t.Fatal(err)
	}
	c.addScreen(ports.OutputInfo{Name: "A", Width: 300, Height: 200})
	c.addScreen(ports.OutputInfo{Name: "B", Width: 300, Height: 200})
	t0 := time.Now()
	next := WindowID(1)
	for _, sc := range c.screens {
		for range 6 {
			sc.mon.AddWindow(next)
			next++
		}
		sc.arrange()
	}
	c.refreshShown()
	for _, sc := range c.screens {
		m := sc.mon
		ws := m.Current()
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
	ctx := context.Background()
	step := func() {
		if err := c.step(ctx, nil); err != nil {
			t.Fatal(err)
		}
		<-c.ch.Scenes
	}
	step()
	for _, sc := range c.screens {
		if len(sc.rects) != 6 || !sc.mon.Current().motion.on || len(sc.shown) < 6 {
			t.Fatalf("setup: rects %d camera %v shown %d", len(sc.rects), sc.mon.Current().motion.on, len(sc.shown))
		}
	}
	n := testing.AllocsPerRun(50, step)
	t.Logf("allocs per step = %v", n)
	for _, sc := range c.screens {
		if len(sc.rects) == 0 || !sc.mon.Current().motion.on {
			t.Fatalf("motions settled during the measure: rects %d", len(sc.rects))
		}
	}
	if n > publishAllocBudget {
		t.Errorf("publish allocs per frame = %v, budget %d", n, publishAllocBudget)
	}
}
