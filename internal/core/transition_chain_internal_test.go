package core

import (
	"context"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// A bind that stops a spring while it acts (Monitor.Focus) must not lose its
// speed: the transition that follows starts from what the snapshot recorded.
func TestChainedActionsKeepVelocity(t *testing.T) {
	for _, tt := range chainCases() {
		t.Run(tt.name, func(t *testing.T) {
			c, ic := indicatorCore(t)
			c.cfg.Animations.On = true
			tt.setup(c)
			settleShown(t, c)
			act := func(actions []Action) {
				before := c.snapshot(ic.now)
				for _, a := range actions {
					c.applyAction(a)
				}
				c.transition(before, ic.now)
				settleShown(t, c)
			}
			act(tt.first)
			if !c.animating() {
				t.Fatal("the first action started no motion")
			}
			ic.now = ic.now.Add(30 * time.Millisecond)
			c.animate(ic.now, nil)
			settleShown(t, c)
			want := tt.velocity(c)
			if want == 0 {
				t.Fatal("no speed to carry over")
			}
			act(tt.second)
			if got := tt.velocity(c); got != want {
				t.Fatalf("velocity %v after the second action, want the carried %v", got, want)
			}
		})
	}
}

// An action between two frames chains from the speed its motion has at the
// action's time (1 ms after the last frame), within 1e-6, not the last
// frame's.
func TestChainedActionsSampleVelocityAtActionTime(t *testing.T) {
	for _, tt := range chainCases() {
		t.Run(tt.name, func(t *testing.T) {
			c, ic := indicatorCore(t)
			c.cfg.Animations.On = true
			tt.setup(c)
			settleShown(t, c)
			before := c.snapshot(ic.now)
			for _, a := range tt.first {
				c.applyAction(a)
			}
			c.transition(before, ic.now)
			settleShown(t, c)
			ic.now = ic.now.Add(30 * time.Millisecond)
			c.animate(ic.now, nil)
			settleShown(t, c)
			m := tt.motion(c)
			last := m.velocity()
			ic.now = ic.now.Add(time.Millisecond)
			// The spring's own speed at now, computed without motion.at.
			tau := time.Duration(float64(ic.now.Sub(m.start)) / m.slow)
			want := m.spring.velocityAt(tau) / m.slow
			if math.Abs(want-last) < 1e-3 {
				t.Fatalf("speed %v did not change in 1 ms (%v): the case checks nothing", last, want)
			}
			before = c.snapshot(ic.now)
			for _, a := range tt.second {
				c.applyAction(a)
			}
			c.transition(before, ic.now)
			if got := tt.velocity(c); math.Abs(got-want) > 1e-6 {
				t.Fatalf("velocity %v after the second action, want the sampled %v (last frame's %v)", got, want, last)
			}
		})
	}
}

type chainCase struct {
	name   string
	setup  func(*Core)
	first  []Action
	second []Action
	// motion is the motion the first action started.
	motion func(*Core) motion
}

// velocity is the speed of the motion the first action started.
func (tt chainCase) velocity(c *Core) float64 { return tt.motion(c).velocity() }

func chainCases() []chainCase {
	return []chainCase{
		{"camera", func(c *Core) {
			m := c.cur().mon
			for id := WindowID(1); id <= 4; id++ {
				m.AddWindow(id)
			}
		}, []Action{ActionFocusColumnLeft, ActionFocusColumnLeft, ActionFocusColumnLeft},
			[]Action{ActionFocusColumnRight, ActionFocusColumnRight, ActionFocusColumnRight},
			func(c *Core) motion { return c.cur().mon.Current().motion }},
		{"workspace switch", func(c *Core) {
			m := c.cur().mon
			m.AddWindow(1)
			m.Focus(1)
			m.AddWindow(2)
			m.Focus(0)
		}, []Action{ActionFocusWorkspaceDown}, []Action{ActionFocusWorkspaceDown},
			func(c *Core) motion { return c.cur().mon.switchMotion }},
		{"rect", func(c *Core) {
			m := c.cur().mon
			m.AddWindow(1)
			m.AddWindow(2)
		}, []Action{ActionMoveColumnLeft}, []Action{ActionMoveColumnRight},
			func(c *Core) motion { return c.cur().rects[1].x }},
	}
}

// The fallback timer guards every animating output: a page flip of one
// output (step with only set) keeps it while another output still animates.
func TestStepKeepsFallbackTimerWhileAnotherOutputAnimates(t *testing.T) {
	c, ic := indicatorCore(t)
	c.cfg.Animations.On = true
	a := c.cur()
	c.addScreen(ports.OutputInfo{Name: "B", Width: 300, Height: 200})
	b := c.screens[1]
	a.mon.AddWindow(1)
	b.mon.AddWindow(2)
	settleShown(t, c)
	for _, sc := range []*screen{a, b} {
		w := sc.mon.Current()
		w.shift, w.motion = 100, c.spring(viewSpring(100, 0), ic.now)
	}
	// The one frame timer is made on the first arm and reset afterwards:
	// a re-arm shows as a Reset on the clock.
	c.armFrame()
	resets := len(ic.resets)
	ic.now = ic.now.Add(16 * time.Millisecond)
	if err := c.step(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	<-c.ch.Scenes
	if c.frameC == nil || len(ic.resets) != resets {
		t.Fatalf("a flip of A re-armed or dropped the fallback timer while B animates: armed %t, resets %d", c.frameC != nil, len(ic.resets)-resets)
	}

	// With nothing else animating the flip's own publish decides.
	b.mon.Current().stopSlide()
	if err := c.step(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	<-c.ch.Scenes
	if c.frameC == nil || len(ic.resets) != resets+1 {
		t.Fatalf("A still animates but its timer was not re-armed: armed %t, resets %d", c.frameC != nil, len(ic.resets)-resets)
	}
	ic.now = ic.now.Add(5 * time.Second)
	if err := c.step(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	<-c.ch.Scenes
	if c.animating() || c.frameC != nil {
		t.Fatalf("settled but the timer runs (%v)", c.frameC != nil)
	}
}

// A window that left the layout while its rect motion ran drops the motion:
// it would ask for frames with nothing to move.
func TestRefreshShownDropsRectsOfWindowsOutOfLayout(t *testing.T) {
	c, ic := indicatorCore(t)
	c.cfg.Animations.On = true
	sc := c.cur()
	sc.mon.AddWindow(1)
	sc.mon.AddWindow(2)
	settleShown(t, c)
	sp := viewSpring(40, 0)
	sc.rects = map[WindowID]rectMotion{
		1:  {x: c.spring(sp, ic.now), dx: 40},
		99: {x: c.spring(sp, ic.now), dx: 40},
	}
	sc.rectsWS = sc.mon.Current()
	settleShown(t, c)
	if _, ok := sc.rects[99]; ok {
		t.Fatal("a window outside the layout keeps its rect motion")
	}
	if _, ok := sc.rects[1]; !ok {
		t.Fatal("a window in the layout lost its rect motion")
	}
}

// A swipe that follows the fingers owns the view: a focus bind pressed
// meanwhile starts no camera motion.
func TestNoCameraTransitionWhileSwipeFollows(t *testing.T) {
	c, ic := indicatorCore(t)
	c.cfg.Animations.On = true
	sc := c.cur()
	for id := WindowID(1); id <= 4; id++ {
		sc.mon.AddWindow(id)
	}
	settleShown(t, c)
	w := sc.mon.Current()
	w.shift = 25
	c.swipe = &swipeGesture{screen: sc, ws: w, mode: swipeColumns}
	before := c.snapshot(ic.now)
	viewX := w.ViewX
	for range 3 {
		c.applyAction(ActionFocusColumnLeft)
	}
	if w.ViewX == viewX {
		t.Fatal("the focus did not move the view: the case checks nothing")
	}
	c.transition(before, ic.now)
	if w.motion.on || w.shift != 25 {
		t.Fatalf("camera motion %v, shift %v: the transition took over the fingers' view", w.motion.on, w.shift)
	}
}

// A switch to or from a workspace with its own size stays settled: no
// monitor-wide slide would be cropped to it. The same switch between
// workspaces of the monitor's size slides.
func TestNoSwitchMotionForSizedWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sized bool
	}{{"sized", true}, {"unsized", false}} {
		t.Run(tc.name, func(t *testing.T) {
			c, ic := indicatorCore(t)
			c.cfg.Animations.On = true
			m := c.cur().mon
			m.AddWindow(1)
			if tc.sized {
				m.Workspaces[1].SetSize(100, 100)
			}
			settleShown(t, c)
			before := c.snapshot(ic.now)
			m.Focus(1)
			c.transition(before, ic.now)
			if got := m.switchMotion.on; got == tc.sized {
				t.Fatalf("switch motion %v with a sized workspace %v", got, tc.sized)
			}
			if tc.sized && m.switchOff != 0 {
				t.Fatalf("switchOff %v left for a framed switch", m.switchOff)
			}
		})
	}
}

// chainedRects is a core with two windows whose first action moves them,
// leaving rect motions running one frame in: the clock moved 30 ms and
// animate sampled them.
func chainedRects(t *testing.T, first Action) (*Core, *indicatorClock) {
	t.Helper()
	c, ic := indicatorCore(t)
	c.cfg.Animations.On = true
	m := c.cur().mon
	m.AddWindow(1)
	m.AddWindow(2)
	settleShown(t, c)
	before := c.snapshot(ic.now)
	c.applyAction(first)
	c.transition(before, ic.now)
	settleShown(t, c)
	ic.now = ic.now.Add(30 * time.Millisecond)
	c.animate(ic.now, nil)
	settleShown(t, c)
	return c, ic
}

// A bind pressed between two frames chains from the speed the springs have
// at the bind's time, not the one of the last frame: a second change 1 ms
// after a frame keeps the sampled speed (within 1e-6) of the component the
// first one started, for a width change and for a move.
func TestChainedRectVelocityIsSampledAtActionTime(t *testing.T) {
	for _, tc := range []struct {
		name          string
		first, second Action
		comp          func(*rectMotion) *motion
		sampled       func(rectShot) float64
	}{
		{"width", ActionMaximizeColumn, ActionMaximizeColumn,
			func(m *rectMotion) *motion { return &m.w }, func(r rectShot) float64 { return r.vel.w }},
		{"position", ActionMoveColumnLeft, ActionMoveColumnRight,
			func(m *rectMotion) *motion { return &m.x }, func(r rectShot) float64 { return r.vel.x }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, ic := chainedRects(t, tc.first)
			sc := c.cur()
			id := WindowID(0)
			for k, rm := range sc.rects {
				if tc.comp(&rm).on {
					id = k
				}
			}
			if id == 0 {
				t.Fatalf("no window with a %s motion in %v", tc.name, sc.rects)
			}
			rm := sc.rects[id]
			m := tc.comp(&rm)
			last := m.velocity()
			ic.now = ic.now.Add(time.Millisecond)

			// The expected speed is the spring's own at now, sampled independently.
			tau := time.Duration(float64(ic.now.Sub(m.start)) / m.slow)
			want := m.spring.velocityAt(tau) / m.slow
			if math.Abs(want-last) < 1e-3 {
				t.Fatalf("the speed did not change in 1 ms (%v to %v): the case checks nothing", last, want)
			}

			before := c.snapshot(ic.now)
			var shot rectShot
			for _, r := range before[0].rects {
				if r.id == id {
					shot = r
				}
			}
			if got := tc.sampled(shot); math.Abs(got-want) > 1e-6 {
				t.Fatalf("sampled speed %v, want %v", got, want)
			}
			// Sampling left the motion alone.
			cur := sc.rects[id]
			if got := tc.comp(&cur).velocity(); got != last {
				t.Fatalf("snapshot moved the motion: speed %v, was %v", got, last)
			}

			c.applyAction(tc.second)
			c.transition(before, ic.now)
			after := sc.rects[id]
			n := tc.comp(&after)
			if !n.on {
				t.Fatalf("the second action left no %s motion", tc.name)
			}
			if v := n.velocity(); math.Abs(v-want) > 1e-6 {
				t.Fatalf("speed %v after the second action, want the sampled %v", v, want)
			}
		})
	}
}

// A retarget keeps the sub-pixel offsets: the old rect is the settled one
// plus the float offsets, not the rounded rect that was drawn.
func TestRetargetKeepsSubPixelOffsets(t *testing.T) {
	c, ic := chainedRects(t, ActionMoveColumnLeft)
	sc := c.cur()
	rm := sc.rects[1]
	if rm.dx == math.Round(rm.dx) {
		t.Fatalf("dx = %v is whole: the case checks nothing", rm.dx)
	}
	settledOld := sc.settledLayout[slices.IndexFunc(sc.settledLayout, func(p Placement) bool { return p.ID == 1 })].Rect

	before := c.snapshot(ic.now)
	var shot rectShot
	for _, r := range before[0].rects {
		if r.id == 1 {
			shot = r
		}
	}
	if shot.rect != settledOld {
		t.Fatalf("shot rect %+v, want the settled %+v", shot.rect, settledOld)
	}
	if shot.off.x != rm.dx || shot.off.w != rm.dw {
		t.Fatalf("shot offsets %+v, want the motion's x %v w %v", shot.off, rm.dx, rm.dw)
	}

	c.applyAction(ActionMoveColumnRight)
	c.transition(before, ic.now)
	settledNew := c.cur().mon.Layout()[slices.IndexFunc(c.cur().mon.Layout(), func(p Placement) bool { return p.ID == 1 })].Rect
	if settledNew == settledOld {
		t.Fatal("the second action did not move window 1")
	}
	got := sc.rects[1]
	// Old rect = settled old + float offset: the new offset from the new
	// settled rect carries the fraction of the old one.
	want := float64(settledOld.X-settledNew.X) + rm.dx
	if math.Abs(got.dx-want) > 1e-9 {
		t.Fatalf("dx = %v after the retarget, want %v (settled %d to %d plus the offset %v)", got.dx, want, settledOld.X, settledNew.X, rm.dx)
	}
	rounded := float64(settledOld.X-settledNew.X) + math.Round(rm.dx)
	if math.Abs(got.dx-rounded) < 1e-6 {
		t.Fatalf("dx = %v equals the rounded rect's: the fraction was lost", got.dx)
	}
}

// An action that moves other windows leaves a running rect motion alone: it
// is not restarted from the rounded rect nor resampled (same start, speed
// and offsets), while the windows the action moved animate.
func TestRectMotionOfUnmovedWindowIsUntouched(t *testing.T) {
	c, ic := indicatorCore(t)
	c.cfg.Animations.On = true
	m := c.cur().mon
	for id := WindowID(1); id <= 3; id++ {
		m.AddWindow(id)
	}
	// 2 and 3 share a column; 1 is a column of its own.
	c.applyAction(ActionConsumeOrExpelLeft)
	settleShown(t, c)
	sc := c.cur()

	// A runs a rect motion, one frame in.
	sp := viewSpring(60.5, 0)
	sc.rects = map[WindowID]rectMotion{1: {x: c.spring(sp, ic.now), dx: 60.5}}
	sc.rectsWS = m.Current()
	ic.now = ic.now.Add(30 * time.Millisecond)
	c.animate(ic.now, nil)
	settleShown(t, c)
	a := sc.rects[1]
	if !a.x.on || a.dx == 0 || a.dx == 60.5 || a.x.velocity() == 0 {
		t.Fatalf("setup: A's motion %+v is not mid-flight", a)
	}

	// B: swapping the stacked 2 and 3 moves those two only.
	ic.now = ic.now.Add(time.Millisecond)
	before := c.snapshot(ic.now)
	c.applyAction(ActionMoveWindowUp)
	c.transition(before, ic.now)
	if got, ok := sc.rects[1]; !ok || got != a {
		t.Fatalf("A's motion changed: %+v, was %+v", got, a)
	}
	for _, id := range []WindowID{2, 3} {
		b, ok := sc.rects[id]
		if !ok || !b.y.on || b.dy == 0 {
			t.Fatalf("window %d does not animate: %+v", id, b)
		}
		if b.x.on {
			t.Fatalf("window %d moved sideways: %+v", id, b)
		}
	}
}
