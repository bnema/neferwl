package core

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/core/motion"
)

// TestAnimateAllocations guards animate: with a camera, a switch and 20 rect
// motions running (fade, dim and scale components, half of them leaving) it
// allocates nothing.
func TestAnimateAllocations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ic := indicatorCore(t)
		c.cfg.Animations.On = true
		sc := c.cur()
		t0 := ic.now
		m := sc.mon
		m.AddWindow(1)
		m.AddWindow(2)

		ws := m.Workspaces[0]
		ws.LandColumns(c.spring(motion.ViewSpring(100, 0), t0))
		ws.DragColumns(float64(ws.View) + (100))
		m.switchView.Motion = c.spring(motion.WorkspaceSpring(100, 0), t0)
		m.switchView.Off = 100
		ws.LandStash(c.spring(motion.WorkspaceSpring(0.5, 0), t0))
		ws.DragStash(float64(ws.StashAt()) + (0.5))
		sc.rects = make(map[WindowID]rectMotion, 20)
		for i := range 20 {
			sp := motion.ViewSpring(50, 0)
			rm := rectMotion{
				x: c.spring(sp, t0), y: c.spring(sp, t0),
				w: c.spring(sp, t0), h: c.spring(sp, t0),
				dx: 50, dy: 50, dw: 50, dh: 50,
				fade: c.spring(motion.LevelSpring(1, 0), t0), df: 1,
				dim: c.spring(motion.LevelSpring(0.5, 0), t0), ddim: 0.5,
				scale: true,
			}
			if i%2 == 0 {
				rm.leaving, rm.left = true, Placement{ID: WindowID(100 + i), Rect: Rect{W: 10, H: 10}}
			}
			sc.rects[WindowID(100+i)] = rm
		}

		// A stash peek's veil moves alone (a navigation): dim-only motions,
		// some going below zero, as an offset from Stash.Dim.
		for i := range 4 {
			var rm rectMotion
			from := 0.5
			if i%2 == 1 {
				from = -0.5
			}
			rm.dim, rm.ddim = c.spring(motion.LevelSpring(from, 0), t0), from
			sc.rects[WindowID(200+i)] = rm
		}

		now := t0.Add(10 * time.Millisecond)
		for name, only := range map[string]*screen{"all": nil, "only": sc} {
			if n := testing.AllocsPerRun(100, func() { c.animate(now, only) }); n != 0 {
				t.Errorf("animate(%s) allocs = %v, want 0", name, n)
			}
		}
		if !ws.Inspect().ColumnsSlide.Motion.On || !ws.Inspect().StashSlide.Motion.On || !m.switchView.Motion.On || len(sc.rects) != 24 {
			t.Fatalf("motions settled: camera %v switch %v rects %d", ws.Inspect().ColumnsSlide.Motion.On, m.switchView.Motion.On, len(sc.rects))
		}

	})
}
