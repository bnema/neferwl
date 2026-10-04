package core

import (
	"testing"
	"time"
)

// TestAnimateAllocations guards animate: with a camera, a switch and 20 rect
// motions running it allocates nothing.
func TestAnimateAllocations(t *testing.T) {
	c, ic := indicatorCore(t)
	c.cfg.Animations.On = true
	sc := c.cur()
	t0 := ic.now
	m := sc.mon
	m.AddWindow(1)
	m.AddWindow(2)

	ws := m.Workspaces[0]
	ws.motion = c.spring(viewSpring(100, 0), t0)
	ws.shift = 100
	m.switchMotion = c.spring(workspaceSpring(100, 0), t0)
	m.switchOff = 100
	sc.rects = make(map[WindowID]rectMotion, 20)
	for i := range 20 {
		sp := viewSpring(50, 0)
		sc.rects[WindowID(100+i)] = rectMotion{
			x: c.spring(sp, t0), y: c.spring(sp, t0),
			w: c.spring(sp, t0), h: c.spring(sp, t0),
			dx: 50, dy: 50, dw: 50, dh: 50,
		}
	}

	now := t0.Add(10 * time.Millisecond)
	for name, only := range map[string]*screen{"all": nil, "only": sc} {
		if n := testing.AllocsPerRun(100, func() { c.animate(now, only) }); n != 0 {
			t.Errorf("animate(%s) allocs = %v, want 0", name, n)
		}
	}
	if !ws.motion.on || !m.switchMotion.on || len(sc.rects) != 20 {
		t.Fatalf("motions settled: camera %v switch %v rects %d", ws.motion.on, m.switchMotion.on, len(sc.rects))
	}
}
