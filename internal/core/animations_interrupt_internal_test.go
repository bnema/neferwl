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
	before := c.snapshot(ic.now)
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

// The last output going away keeps its screen as a placeholder: none of its
// springs (rect motions, a workspace switch) may outlive the output, or the
// fallback timer would wake core for a screen nothing draws. When an output
// comes back, a new move animates and lands from a fresh Seq.
func TestLastOutputUnplugMidRectMotion(t *testing.T) {
	c, ic := indicatorCore(t)
	sc := c.cur()
	movingRects(t, c, ic, sc)
	// A workspace switch spring runs too.
	m := sc.mon
	m.switchView.off, m.switchView.motion = 0.5, c.spring(workspaceSpring(0.5, 0), ic.now)
	if !m.switchView.motion.on || !sc.springing() {
		t.Fatal("no workspace switch to interrupt")
	}
	c.removeScreen("A")
	if sc.mon.springing() || len(sc.rects) != 0 || sc.rectsWS != nil || sc.mon.switchView.motion.on || sc.mon.switchView.off != 0 {
		t.Fatalf("the placeholder keeps springs: monitor %t, rects %d, switch %t (%v)", sc.mon.springing(), len(sc.rects), sc.mon.switchView.motion.on, sc.mon.switchView.off)
	}
	if err := c.publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	gone := (<-c.ch.Scenes)[0]
	if c.animating() || c.frameC != nil {
		t.Fatalf("the placeholder keeps asking for frames: animating %t, timer %t", c.animating(), c.frameC != nil)
	}

	// The output comes back: its windows are where the move left them.
	c.addScreen(ports.OutputInfo{Name: "A", Width: 300, Height: 200})
	settled := indicatorScene(t, c)
	if settled.Seq <= gone.Seq {
		t.Fatalf("scene after the replug reuses Seq %d (placeholder %d)", settled.Seq, gone.Seq)
	}
	x := func(s ports.Scene, id WindowID) int {
		for _, w := range s.Windows {
			if w.ID == id {
				return w.Rect.X
			}
		}
		t.Fatalf("window %d not in the scene", id)
		return 0
	}
	if x(settled, 2) != 0 || x(settled, 1) != 150 {
		t.Fatalf("after the replug 1 at %d and 2 at %d, want the settled 150 and 0", x(settled, 1), x(settled, 2))
	}
	if c.animating() || len(c.cur().rects) != 0 {
		t.Fatal("a motion runs after the replug")
	}

	// A new move animates from the settled rects and lands.
	before := c.snapshot(ic.now)
	c.applyAction(ActionMoveColumnRight)
	c.transition(before, ic.now)
	start := indicatorScene(t, c)
	if x(start, 1) != 150 || x(start, 2) != 0 {
		t.Fatalf("right after the move 1 at %d and 2 at %d, want the old 150 and 0", x(start, 1), x(start, 2))
	}
	ic.now = ic.now.Add(16 * time.Millisecond)
	if err := c.step(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	mid := (<-c.ch.Scenes)[0]
	if got := x(mid, 1); got >= 150 || got <= 0 {
		t.Fatalf("window 1 at %d on the first frame, want between 0 and 150", got)
	}
	if mid.Seq == start.Seq {
		t.Fatalf("an animating frame reuses Seq %d", mid.Seq)
	}
	ic.now = ic.now.Add(5 * time.Second)
	if err := c.step(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	end := (<-c.ch.Scenes)[0]
	if x(end, 1) != 0 || x(end, 2) != 150 || end.Seq == mid.Seq {
		t.Fatalf("landed 1 at %d and 2 at %d (Seq %d after %d), want 0 and 150 and a fresh Seq", x(end, 1), x(end, 2), end.Seq, mid.Seq)
	}
	if c.animating() || c.frameC != nil {
		t.Fatal("still animating after the landing")
	}
}
