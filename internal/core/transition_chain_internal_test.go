package core

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// settleShown publishes so the screens' shown layouts are current.
func settleShown(t *testing.T, c *Core) {
	t.Helper()
	indicatorScene(t, c)
}

// A bind that stops a spring while it acts (Monitor.Focus) must not lose its
// speed: the transition that follows starts from what the snapshot recorded.
func TestChainedActionsKeepVelocity(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*Core)
		first  []Action
		second []Action
		// velocity is the speed of the motion the first action started.
		velocity func(*Core) float64
	}{
		{"camera", func(c *Core) {
			m := c.cur().mon
			for id := WindowID(1); id <= 4; id++ {
				m.AddWindow(id)
			}
		}, []Action{ActionFocusColumnLeft, ActionFocusColumnLeft, ActionFocusColumnLeft},
			[]Action{ActionFocusColumnRight, ActionFocusColumnRight, ActionFocusColumnRight},
			func(c *Core) float64 { return c.cur().mon.Current().motion.velocity() }},
		{"workspace switch", func(c *Core) {
			m := c.cur().mon
			m.AddWindow(1)
			m.Focus(1)
			m.AddWindow(2)
			m.Focus(0)
		}, []Action{ActionFocusWorkspaceDown}, []Action{ActionFocusWorkspaceDown},
			func(c *Core) float64 { return c.cur().mon.switchMotion.velocity() }},
		{"rect", func(c *Core) {
			m := c.cur().mon
			m.AddWindow(1)
			m.AddWindow(2)
		}, []Action{ActionMoveColumnLeft}, []Action{ActionMoveColumnRight},
			func(c *Core) float64 { return c.cur().rects[1].x.velocity() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, ic := indicatorCore(t)
			c.cfg.Animations.On = true
			tt.setup(c)
			settleShown(t, c)
			act := func(actions []Action) {
				before := c.snapshot()
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
	c.armFrame()
	armed := c.frameC
	ic.now = ic.now.Add(16 * time.Millisecond)
	if err := c.step(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	<-c.ch.Scenes
	if c.frameC == nil || c.frameC != armed {
		t.Fatal("a flip of A re-armed or dropped the fallback timer while B animates")
	}

	// With nothing else animating the flip's own publish decides.
	b.mon.Current().stopSlide()
	if err := c.step(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	<-c.ch.Scenes
	if c.frameC == nil || c.frameC == armed {
		t.Fatal("A still animates but its timer was not re-armed")
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
	before := c.snapshot()
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
			before := c.snapshot()
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
