package core

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// BenchmarkRefreshShown is one publish's layout build on a screen of 24
// windows, 20 of them with a rect motion and 6 more leaving: the cost of
// matching the motions to the layout every frame. The layout is built in the
// screen's reused buffers: 0 allocs/op (33 before).
func BenchmarkRefreshShown(b *testing.B) {
	var cfg ports.Config
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 4
	cfg.Animations.On = true
	cfg.Animations.Slowdown = 10
	c, err := New(cfg, Channels{Scenes: make(chan []ports.Scene, 1), Commands: make(chan ports.ClientCommand, 64)}, Options{})
	if err != nil {
		b.Fatal(err)
	}
	c.addScreen(ports.OutputInfo{Name: "A", Width: 3000, Height: 2000})
	sc := c.cur()
	for id := WindowID(1); id <= 30; id++ {
		sc.mon.AddWindow(id)
	}
	sc.arrange()
	c.refreshShown()
	t0 := time.Now()
	sc.rects = make(map[WindowID]rectMotion, 26)
	sp := viewSpring(100, 0)
	for i, p := range sc.settledLayout {
		if i >= 20 {
			break
		}
		sc.rects[p.ID] = rectMotion{
			x: c.spring(sp, t0), y: c.spring(sp, t0), w: c.spring(sp, t0), h: c.spring(sp, t0),
			dx: 100, dy: 100, dw: 100, dh: 100,
			fade: c.spring(levelSpring(0.5, 0), t0), df: 0.5, scale: true,
		}
	}
	for _, p := range sc.settledLayout[24:] {
		sc.mon.RemoveWindow(p.ID)
		c.leaveFrom(sc, p, nil, t0)
	}
	sc.rectsWS = sc.mon.Current()
	c.refreshShown()
	if len(sc.rects) != 26 {
		b.Fatalf("rects %d", len(sc.rects))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		c.refreshShown()
	}
	if len(sc.rects) != 26 {
		b.Fatalf("rects %d after", len(sc.rects))
	}
}
