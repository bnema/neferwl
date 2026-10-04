package core

import (
	"context"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

// movingRects makes two windows on sc swap places and returns with their
// rect motions running, one frame in (the clock moved 16 ms and stepped).
func movingRects(t *testing.T, c *Core, ic *indicatorClock, sc *screen) {
	t.Helper()
	c.cfg.Animations.On = true
	sc.mon.AddWindow(1)
	sc.mon.AddWindow(2)
	settleShown(t, c)
	c.focusScreen = c.screenIndex(sc.name())
	before := c.snapshot()
	c.applyAction(ActionMoveColumnLeft)
	c.transition(before, ic.now)
	if len(sc.rects) != 2 {
		t.Fatalf("%d rect motions after the move, want 2", len(sc.rects))
	}
	indicatorScene(t, c)
	ic.now = ic.now.Add(16 * time.Millisecond)
	if err := c.step(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	<-c.ch.Scenes
	if rm := sc.rects[1]; rm.dx == 0 || !rm.on() {
		t.Fatalf("window 1 offset %v (running %t) one frame in: not mid-motion", rm.dx, rm.on())
	}
}

// A session lock in the middle of a rect motion stops every motion and the
// frame timer, drops the screens' last scene and shown layouts, and the
// scene after the unlock is the settled one with a Seq nothing used before.
func TestSessionLockMidRectMotion(t *testing.T) {
	c, ic := indicatorCore(t)
	state := ports.SecurityState{}
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState { return state })
	c.ch.Security = gate
	sc := c.cur()
	movingRects(t, c, ic, sc)
	if !c.animating() || c.frameC == nil || sc.last.Seq == 0 {
		t.Fatalf("nothing to interrupt: animating %t, timer %t, last seq %d", c.animating(), c.frameC != nil, sc.last.Seq)
	}
	seq := sc.last.Seq

	state = ports.SecurityState{Generation: 1, Protected: true}
	if err := c.publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	locked := (<-c.ch.Scenes)[0]
	if len(locked.Windows) != 0 || len(locked.Separators) != 0 || locked.Seq <= seq {
		t.Fatalf("locked scene shows the desktop or reuses a Seq (%d <= %d): %+v", locked.Seq, seq, locked)
	}
	if len(sc.rects) != 0 || sc.rectsWS != nil || sc.springing() || c.animating() || c.frameC != nil {
		t.Fatalf("motion survived the lock: rects %d, springing %t, animating %t, timer %t", len(sc.rects), sc.springing(), c.animating(), c.frameC != nil)
	}
	if sc.last.Seq != 0 || sc.shown != nil || sc.settledLayout != nil {
		t.Fatalf("lock kept the desktop scene: last seq %d, shown %v, settled %v", sc.last.Seq, sc.shown != nil, sc.settledLayout != nil)
	}

	// Unlocked: the windows are where the move left them, nothing runs.
	state = ports.SecurityState{Generation: 2}
	s := indicatorScene(t, c)
	if s.Seq <= locked.Seq {
		t.Fatalf("unlocked scene Seq %d reuses the locked one %d", s.Seq, locked.Seq)
	}
	var got1, got2 Rect
	for _, w := range s.Windows {
		switch w.ID {
		case 1:
			got1 = w.Rect
		case 2:
			got2 = w.Rect
		}
	}
	// 300x200 output, two columns of 150: the move put 2 first.
	if got2.X != 0 || got1.X != 150 {
		t.Fatalf("after the unlock 1 at %+v and 2 at %+v, want the settled 150 and 0", got1, got2)
	}
	if c.animating() || len(sc.rects) != 0 || c.frameC != nil {
		t.Fatalf("a motion runs after the unlock: animating %t, rects %d, timer %t", c.animating(), len(sc.rects), c.frameC != nil)
	}
}

// An output unplugged in the middle of its windows' rect motion: no panic,
// its motions go with it, a late page flip for it does nothing, and the
// remaining output keeps no motion of the gone one.
func TestOutputUnplugMidRectMotion(t *testing.T) {
	c, ic := indicatorCore(t)
	a := c.cur()
	c.addScreen(ports.OutputInfo{Name: "B", Width: 300, Height: 200})
	b := c.screens[1]
	movingRects(t, c, ic, b)
	c.removeScreen("B")
	if c.screenIndex("B") >= 0 || len(c.screens) != 1 {
		t.Fatalf("B still plugged: %d screens", len(c.screens))
	}
	if len(a.rects) != 0 || a.springing() {
		t.Fatalf("A inherited the motions of B: rects %d, springing %t", len(a.rects), a.springing())
	}
	if c.sliding("B") {
		t.Fatal("the gone output still slides")
	}
	// B's windows are guests of A now: the next publish and a stray flip are fine.
	if err := c.publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-c.ch.Scenes
	ic.now = ic.now.Add(5 * time.Second)
	if err := c.step(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	<-c.ch.Scenes
	if c.animating() || c.frameC != nil {
		t.Fatalf("still animating after the unplug and a settle: animating %t, timer %t", c.animating(), c.frameC != nil)
	}
}

// The last output going away keeps its screen as a placeholder: its rect
// motions must not outlive it (no frame would ever settle them but the
// fallback timer).
func TestLastOutputUnplugMidRectMotion(t *testing.T) {
	c, ic := indicatorCore(t)
	sc := c.cur()
	movingRects(t, c, ic, sc)
	c.removeScreen("A")
	if err := c.publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-c.ch.Scenes
	if len(sc.rects) != 0 || c.animating() {
		t.Fatalf("the placeholder keeps motions: rects %d, animating %t", len(sc.rects), c.animating())
	}
}
