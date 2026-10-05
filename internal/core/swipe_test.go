package core_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/ports"
)

// swipeRig runs core on one 800x600 output with no gaps or border, a
// clock the test moves, and page flips the test sends.
type swipeRig struct {
	*multiRig
	frames chan ports.OutputFrame
	clk    *stepClock
	// at is the device time of the next swipe event.
	at time.Duration
	// outs are the plugged outputs, for landed.
	outs []string
}

var wide = ports.OutputInfo{Name: "DP-1", Width: 800, Height: 600, RefreshMilli: 60000}

func startSwipe(t *testing.T, edit func(*ports.Config)) *swipeRig {
	t.Helper()
	return startSwipeOn(t, edit, wide)
}

// startSwipeOn is startSwipe with the given outputs plugged in order.
func startSwipeOn(t *testing.T, edit func(*ports.Config), outs ...ports.OutputInfo) *swipeRig {
	t.Helper()
	cfg := altCmdDefaults()
	cfg.Border.Width = 0
	cfg.Layout.Gaps = 0
	cfg.Terminal.AutoOpen = "never"
	// The rig steps animations frame by frame: they are on unless edit
	// turns them off.
	cfg.Animations.On = true
	if edit != nil {
		edit(&cfg)
	}
	r := &swipeRig{frames: make(chan ports.OutputFrame), clk: newStepClock(t)}
	r.multiRig = &multiRig{
		client: make(chan ports.ClientEvent, 16), input: make(chan ports.InputEvent, 64),
		output: make(chan ports.OutputEvent, 4), reload: make(chan ports.ConfigChanged, 4),
		commands: make(chan ports.ClientCommand, 1024), scenes: make(chan []ports.Scene, 1), spawn: make(chan ports.SpawnRequest, 16), state: make(chan ports.State, 1), cfg: cfg,
	}
	c, err := core.New(cfg, core.Channels{Client: r.client, Input: r.input, Output: r.output, Config: r.reload, Commands: r.commands, Scenes: r.scenes, Spawn: r.spawn, State: r.state, Clock: r.clk.clock, Frames: r.frames})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	for _, o := range outs {
		r.plug(t, o)
		r.outs = append(r.outs, o.Name)
	}
	return r
}

func (r *swipeRig) advance(d time.Duration) {
	r.clk.advance(d)
}

// mapWindow maps a window and, with animations on, lands its entrance (and
// the neighbours' re-flow) so the scene is the settled one: the tests of a
// swipe rig look at what the map leaves. The entrance itself is checked on
// the raw multiRig.mapWindow (TestMap*).
func (r *swipeRig) mapWindow(t *testing.T, id ports.WindowID) []ports.Scene {
	t.Helper()
	set := r.multiRig.mapWindow(t, id)
	if landed, ok := r.settleAll(t, r.outs...); ok {
		return landed
	}
	return set
}

// begin starts a swipe; nothing moves until it picks an axis.
func (r *swipeRig) begin() {
	r.at += time.Second
	r.input <- ports.SwipeBegin{Fingers: 3, Time: r.at}
}

// move sends one swipe update and returns the scene it produced.
func (r *swipeRig) move(t *testing.T, dx, dy float64) ports.Scene {
	t.Helper()
	r.at += 8 * time.Millisecond
	r.input <- ports.SwipeUpdate{DX: dx, DY: dy, Time: r.at}
	return scene(t, r.scenes)
}

func (r *swipeRig) end(t *testing.T, cancelled bool) ports.Scene {
	t.Helper()
	r.input <- ports.SwipeEnd{Cancelled: cancelled, Time: r.at}
	return scene(t, r.scenes)
}

// frame moves the clock by d, sends a flip and returns the next scene.
func (r *swipeRig) frame(t *testing.T, d time.Duration) ports.Scene {
	t.Helper()
	r.advance(d)
	r.frames <- ports.OutputFrame{Output: wide.Name}
	return scene(t, r.scenes)
}

// settle moves the clock past any spring and returns the settled scene.
func (r *swipeRig) settle(t *testing.T) ports.Scene {
	t.Helper()
	return r.frame(t, 5*time.Second)
}

// settleAll moves the clock past every spring, flips the outputs and
// returns the scene set that results (ok false: nothing was animating).
func (r *swipeRig) settleAll(t *testing.T, outputs ...string) ([]ports.Scene, bool) {
	t.Helper()
	return r.clk.settle(t, r.frames, r.scenes, outputs...)
}

// flick sends a quick swipe of n updates of (dx, dy) and lifts the
// fingers; it returns the scene right after the lift.
func (r *swipeRig) flick(t *testing.T, n int, dx, dy float64) ports.Scene {
	t.Helper()
	r.begin()
	for range n {
		r.move(t, dx, dy)
	}
	return r.end(t, false)
}

// workspaces puts window 1 on the first workspace and 2 on the second,
// showing the first.
func (r *swipeRig) workspaces(t *testing.T) {
	t.Helper()
	r.mapWindow(t, 1)
	r.keySettled(t, "Next")
	r.mapWindow(t, 2)
	r.keySettled(t, "Prior")
}

// keySettled presses a workspace key and waits for the slide it starts, so
// the next step begins from the settled view.
func (r *swipeRig) keySettled(t *testing.T, sym string) {
	t.Helper()
	r.key(t, sym, ports.ModAlt)
	if r.cfg.Animations.On {
		r.settle(t)
	}
}

func rectOf(s ports.Scene, id ports.WindowID) (ports.Rect, bool) {
	for _, w := range s.Windows {
		if w.ID == id && !w.Hidden {
			return w.Rect, true
		}
	}
	return ports.Rect{}, false
}

func (r *swipeRig) focused(t *testing.T) ports.WindowID {
	t.Helper()
	var id ports.WindowID
	for len(r.commands) > 0 {
		if v, ok := (<-r.commands).(ports.FocusWindow); ok {
			id = v.ID
		}
	}
	return id
}

// threeColumns maps three windows of half the width; the view shows 2
// and 3, with 3 focused. It returns the last scene.
func threeColumns(t *testing.T, r *swipeRig) ports.Scene {
	t.Helper()
	var s []ports.Scene
	for id := ports.WindowID(1); id <= 3; id++ {
		s = r.mapWindow(t, id)
	}
	return s[0]
}

func TestSwipeFollowsFingers(t *testing.T) {
	r := startSwipe(t, nil)
	before, _ := rectOf(threeColumns(t, r), 2)
	r.begin()
	// Under the 16 unit threshold the swipe has not picked its axis:
	// that movement is not applied (as in GNOME Shell and niri).
	r.input <- ports.SwipeUpdate{DX: -10, Time: r.at}
	s := r.move(t, -10, 0)
	after, _ := rectOf(s, 2)
	// 10 units of 1200 per 800 px view are 7 px; near the resting view
	// the detent holds the view back a little.
	if d := after.X - before.X; d <= 0 || d >= 7 {
		t.Fatalf("column moved %d px with the fingers", d)
	}
	// Halfway to the next column edge (400 px: 600 units) the view has
	// caught up with the fingers; a full step shows whole.
	s = r.move(t, -290, 0)
	if got, _ := rectOf(s, 2); got.X-before.X != 200 {
		t.Fatalf("column moved %d px halfway", got.X-before.X)
	}
	s = r.move(t, -300, 0)
	if got, _ := rectOf(s, 2); got.X-before.X != 400 {
		t.Fatalf("column moved %d px after a full step", got.X-before.X)
	}
}

func TestSwipeFlickLandsOnColumnWithSpring(t *testing.T) {
	r := startSwipe(t, nil)
	threeColumns(t, r)
	// Focus is on 3 (right); the view shows 2 and 3. Flick right to left
	// fingers (content moves left in classic scroll): view goes left.
	r.focused(t)
	s := r.flick(t, 10, -40, 0)
	mid, ok := rectOf(s, 1)
	if !ok {
		t.Fatal("column 1 not shown while the view slides")
	}
	if mid.X == 0 {
		t.Fatal("view jumped: no slide")
	}
	s = r.frame(t, 16*time.Millisecond)
	next, _ := rectOf(s, 1)
	if next.X <= mid.X || next.X > 0 {
		t.Fatalf("spring does not approach the target: %d then %d", mid.X, next.X)
	}
	s = r.settle(t)
	if got, _ := rectOf(s, 1); got.X != 0 {
		t.Fatalf("landed at %d, want column 1 at the left edge", got.X)
	}
	if id := r.focused(t); id != 1 && id != 2 {
		t.Fatalf("focus %d after swiping left", id)
	}
}

func TestSwipeNaturalScrollInverts(t *testing.T) {
	for _, natural := range []bool{false, true} {
		r := startSwipe(t, func(c *ports.Config) { c.Touchpad.NaturalScroll = natural })
		threeColumns(t, r)
		dx := -40.0
		if natural {
			dx = 40
		}
		r.flick(t, 10, dx, 0)
		s := r.settle(t)
		if got, _ := rectOf(s, 1); got.X != 0 {
			t.Fatalf("natural=%t: column 1 at %d", natural, got.X)
		}
	}
}

func TestSwipeWorkspaceSlides(t *testing.T) {
	r := startSwipe(t, nil)
	r.workspaces(t)
	r.begin()
	s := r.move(t, 0, 30)
	one, ok1 := rectOf(s, 1)
	two, ok2 := rectOf(s, 2)
	if !ok1 || !ok2 {
		t.Fatalf("both workspaces show while sliding: %v %v", ok1, ok2)
	}
	if one.Y >= 0 || two.Y <= 0 || two.Y-one.Y != 600 {
		t.Fatalf("slide positions %d %d", one.Y, two.Y)
	}
	for range 8 {
		r.move(t, 0, 30)
	}
	s = r.end(t, false)
	if _, ok := rectOf(s, 2); !ok {
		t.Fatal("target workspace hidden while landing")
	}
	s = r.settle(t)
	if _, ok := rectOf(s, 1); ok {
		t.Fatal("old workspace still shown")
	}
	if got, _ := rectOf(s, 2); got.Y != 0 {
		t.Fatalf("workspace 2 at %d", got.Y)
	}
}

func TestSwipeWorkspaceCancelReturns(t *testing.T) {
	r := startSwipe(t, nil)
	r.workspaces(t)
	r.begin()
	for range 10 {
		r.move(t, 0, 40)
	}
	r.end(t, true)
	s := r.settle(t)
	if got, ok := rectOf(s, 1); !ok || got.Y != 0 {
		t.Fatalf("cancelled swipe left workspace 1 at %v %t", got, ok)
	}
}

func TestSwipeFixedOverflowRunsFocusAction(t *testing.T) {
	r := startSwipe(t, func(c *ports.Config) { c.Layout.Overflow = "fixed" })
	r.mapWindow(t, 1)
	r.mapWindow(t, 2)
	r.focused(t)
	// The view cannot slide: nothing moves until the fingers lift.
	r.begin()
	for range 10 {
		r.at += 8 * time.Millisecond
		r.input <- ports.SwipeUpdate{DX: -40, Time: r.at}
	}
	r.end(t, false)
	if id := r.focused(t); id != 1 {
		t.Fatalf("fixed overflow swipe left focuses %d", id)
	}
}

func TestSwipeShortSwipeDoesNothingInFixedOverflow(t *testing.T) {
	r := startSwipe(t, func(c *ports.Config) { c.Layout.Overflow = "fixed" })
	r.mapWindow(t, 1)
	r.mapWindow(t, 2)
	r.focused(t)
	r.begin()
	r.input <- ports.SwipeUpdate{DX: -20, Time: r.at + time.Second}
	r.input <- ports.SwipeEnd{Time: r.at + 2*time.Second}
	r.key(t, "Return", 0)
	if id := r.focused(t); id != 0 {
		t.Fatalf("slow short swipe moved focus to %d", id)
	}
}

func TestKeyDuringSlideRetargets(t *testing.T) {
	r := startSwipe(t, nil)
	threeColumns(t, r)
	s := r.flick(t, 10, -40, 0)
	landing, _ := rectOf(s, 2)
	// Back to column 3: the view heads there from where it is, no jump.
	r.key(t, "Right", ports.ModAlt)
	s = r.key(t, "Right", ports.ModAlt)[0]
	if got, _ := rectOf(s, 2); got.X != landing.X {
		t.Fatalf("view jumped from %d to %d", landing.X, got.X)
	}
	// The retargeted spring starts on the next frame.
	r.frame(t, 16*time.Millisecond)
	s = r.settle(t)
	if got, _ := rectOf(s, 3); got.X != 400 {
		t.Fatalf("column 3 landed at %d", got.X)
	}
}

func TestSwipeFromEmptyWorkspaceLandsWithoutJump(t *testing.T) {
	r := startSwipe(t, nil)
	r.workspaces(t)
	// Workspace 3 is the trailing empty one; start there and swipe up to 2.
	r.keySettled(t, "Next")
	r.keySettled(t, "Next")
	r.begin()
	var s ports.Scene
	for range 6 {
		s = r.move(t, 0, -40)
	}
	before, ok := rectOf(s, 2)
	if !ok {
		t.Fatal("workspace 2 not sliding in")
	}
	// Landing drops no numbered workspace here (the trailing one stays),
	// but the view must not jump either way.
	s = r.end(t, false)
	after, _ := rectOf(s, 2)
	if after.Y != before.Y {
		t.Fatalf("landing jumped from %d to %d", before.Y, after.Y)
	}
	if got, _ := rectOf(r.settle(t), 2); got.Y != 0 {
		t.Fatalf("workspace 2 landed at %d", got.Y)
	}
}

func TestSwipeLandingKeepsViewWhenEmptyWorkspaceDrops(t *testing.T) {
	r := startSwipe(t, nil)
	// 1 on the first workspace, 2 on the third; the second is empty and
	// active, so leaving it drops it and renumbers the third.
	r.mapWindow(t, 1)
	r.key(t, "Next", ports.ModAlt)
	r.key(t, "Next", ports.ModAlt)
	r.mapWindow(t, 2)
	r.key(t, "Prior", ports.ModAlt)
	r.begin()
	var s ports.Scene
	for range 4 {
		s = r.move(t, 0, 20)
	}
	before, ok := rectOf(s, 2)
	if !ok {
		t.Fatal("workspace 3 not sliding in")
	}
	s = r.end(t, false)
	after, ok := rectOf(s, 2)
	if !ok || after.Y != before.Y {
		t.Fatalf("landing moved workspace 3 from %d to %d (shown %t)", before.Y, after.Y, ok)
	}
	if got, _ := rectOf(r.settle(t), 2); got.Y != 0 {
		t.Fatalf("landed at %d", got.Y)
	}
}

func TestSwipeDropsWhenWorkspacesChange(t *testing.T) {
	r := startSwipe(t, nil)
	r.workspaces(t)
	r.begin()
	r.move(t, 0, 30)
	// A key switches workspace mid-swipe: the swipe lets go at once.
	s := r.key(t, "Next", ports.ModAlt)[0]
	if got, ok := rectOf(s, 2); !ok || got.Y == 0 {
		t.Fatalf("workspace 2 at %v %t after the key, want the old position", got, ok)
	}
	s = r.settle(t)
	if got, ok := rectOf(s, 2); !ok || got.Y != 0 {
		t.Fatalf("workspace 2 at %v %t after the key", got, ok)
	}
	if _, ok := rectOf(s, 1); ok {
		t.Fatal("workspace 1 still sliding")
	}
	// The rest of the swipe moves nothing: its end leaves the view as is.
	r.at += 8 * time.Millisecond
	r.input <- ports.SwipeUpdate{DY: 30, Time: r.at}
	if got, ok := rectOf(r.end(t, false), 2); !ok || got.Y != 0 {
		t.Fatalf("workspace 2 at %v %t after the swipe", got, ok)
	}
}

func TestSlideMovesPointerFocus(t *testing.T) {
	r := startSwipe(t, nil)
	threeColumns(t, r)
	// The pointer rests over column 2 (x 100: the view shows 2 and 3).
	r.input <- ports.PointerMotion{X: 100, Y: 300}
	r.drain()
	r.flick(t, 10, -40, 0)
	r.settle(t)
	var focus ports.PointerFocus
	var motion ports.PointerMotionTo
	// Core sends the pointer update right after the scene.
	for _, v := range r.sent() {
		switch v := v.(type) {
		case ports.PointerFocus:
			focus = v
		case ports.PointerMotionTo:
			motion = v
		}
	}
	// Column 1 slid under the still pointer; it follows the window.
	if focus.ID != 1 {
		t.Fatalf("pointer focus %+v", focus)
	}
	if motion.ID != 1 || motion.X != 100 || motion.DX != 0 {
		t.Fatalf("last motion %+v", motion)
	}
}

func TestSwipeBeginWithoutEndSettlesTheFirst(t *testing.T) {
	r := startSwipe(t, nil)
	threeColumns(t, r)
	r.begin()
	s := r.move(t, -40, 0)
	moved, _ := rectOf(s, 2)
	// A begin without the previous end: the first swipe springs back.
	r.begin()
	scene(t, r.scenes)
	s = r.settle(t)
	if got, _ := rectOf(s, 2); got.X == moved.X || got.X != 0 {
		t.Fatalf("first swipe left column 2 at %d", got.X)
	}
}

func TestSwipeIgnoredAfterSwitchingAwayAndBack(t *testing.T) {
	r := startSwipe(t, nil)
	before, _ := rectOf(threeColumns(t, r), 2)
	r.begin()
	r.move(t, -30, 0)
	r.key(t, "Next", ports.ModAlt)
	r.key(t, "Prior", ports.ModAlt)
	// The same workspace is back, but the swipe was let go.
	r.at += 8 * time.Millisecond
	r.input <- ports.SwipeUpdate{DX: -30, Time: r.at}
	if got, _ := rectOf(r.end(t, false), 2); got != before {
		t.Fatalf("column 2 at %v, want %v", got, before)
	}
}

func TestSwipeKeptWhenAnotherOutputSwitchesWorkspace(t *testing.T) {
	r := startSwipe(t, nil)
	before, _ := rectOf(threeColumns(t, r), 2)
	r.plug(t, ports.OutputInfo{Name: "DP-2", Width: 800, Height: 600, RefreshMilli: 60000})
	// Window 4 on DP-2, so it has a workspace below to switch to.
	r.input <- ports.PointerMotion{X: 1000, Y: 300}
	scene(t, r.scenes)
	r.mapWindow(t, 4)
	r.input <- ports.PointerMotion{X: 100, Y: 300}
	scene(t, r.scenes)
	r.drain()
	r.begin()
	r.move(t, -30, 0)
	// The pointer goes to DP-2 and a bind switches its workspace.
	r.input <- ports.PointerMotion{X: 1000, Y: 300}
	scene(t, r.scenes)
	r.key(t, "Next", ports.ModAlt)
	// DP-2 slides to its other workspace on its own flips.
	r.advance(5 * time.Second)
	r.frames <- ports.OutputFrame{Output: "DP-2"}
	for _, sc := range receive(t, r.scenes) {
		if _, ok := rectOf(sc, 4); ok {
			t.Fatal("DP-2 did not switch workspace")
		}
	}
	// The swipe on DP-1 still follows the fingers.
	var got ports.Rect
	for _, sc := range receiveMove(t, r, -570) {
		if sc.Output == wide.Name {
			got, _ = rectOf(sc, 2)
		}
	}
	// 600 units at 2/3 px each: a full step of 400 px.
	if got.X-before.X != 400 {
		t.Fatalf("column 2 moved %d px", got.X-before.X)
	}
}

// receiveMove sends one swipe update and returns every output's scene.
func receiveMove(t *testing.T, r *swipeRig, dx float64) []ports.Scene {
	t.Helper()
	r.at += 8 * time.Millisecond
	r.input <- ports.SwipeUpdate{DX: dx, Time: r.at}
	return receive(t, r.scenes)
}

func TestDiscreteSwipeSkippedWhenPointerChangesOutput(t *testing.T) {
	r := startSwipe(t, func(c *ports.Config) { c.Layout.Overflow = "fixed" })
	r.mapWindow(t, 1)
	r.mapWindow(t, 2)
	r.plug(t, ports.OutputInfo{Name: "DP-2", Width: 800, Height: 600, RefreshMilli: 60000})
	// Windows 3 and 4 on DP-2, 4 focused there.
	r.input <- ports.PointerMotion{X: 1000, Y: 300}
	scene(t, r.scenes)
	r.mapWindow(t, 3)
	r.mapWindow(t, 4)
	// The swipe starts on DP-1 ...
	r.input <- ports.PointerMotion{X: 100, Y: 300}
	scene(t, r.scenes)
	r.begin()
	for range 10 {
		r.at += 8 * time.Millisecond
		r.input <- ports.SwipeUpdate{DX: -40, Time: r.at}
	}
	// ... and the pointer takes the focus to DP-2 before the fingers lift.
	r.input <- ports.PointerMotion{X: 1000, Y: 300}
	scene(t, r.scenes)
	r.drain()
	r.input <- ports.SwipeEnd{Time: r.at}
	for _, v := range r.sent() {
		if f, ok := v.(ports.FocusWindow); ok && f.ID == 3 {
			t.Fatal("swipe from DP-1 moved the focus on DP-2")
		}
	}
}

// drain waits for core to handle what was sent, then empties commands.
func (r *swipeRig) drain() {
	for range r.sent() {
	}
}

// sent waits for core to handle what was sent and returns the commands it
// sent meanwhile (the sync flip is a barrier, see published).
func (r *swipeRig) sent() []ports.ClientCommand {
	send(r.frames, ports.OutputFrame{Output: "sync"})
	var out []ports.ClientCommand
	for {
		select {
		case v := <-r.commands:
			out = append(out, v)
		default:
			return out
		}
	}
}

// In the overview a three-finger swipe does not slide the view: nothing
// is drawn while the fingers move, and when they lift the selection moves
// one column, the previews where they were.
func TestSwipeInOverview(t *testing.T) {
	r := startSwipe(t, nil)
	threeColumns(t, r)
	opened := r.key(t, "o", ports.ModAlt)[0]
	selected := func(s ports.Scene) ports.WindowID {
		for _, w := range s.Windows {
			if w.Focused && w.Preview > 0 {
				return w.ID
			}
		}
		return 0
	}
	r.begin()
	for range 3 {
		r.at += 8 * time.Millisecond
		r.input <- ports.SwipeUpdate{DX: -40, Time: r.at}
	}
	s := r.end(t, false)
	if id := selected(s); id != 2 {
		t.Fatalf("selected %d after a left swipe, want 2", id)
	}
	before, _ := rectOf(opened, 2)
	if after, _ := rectOf(s, 2); before != after {
		t.Fatalf("preview moved: %+v then %+v", before, after)
	}
	// A small swipe does nothing: the l after it moves from 2 to 3.
	r.begin()
	r.at += 8 * time.Millisecond
	r.input <- ports.SwipeUpdate{DX: -20, Time: r.at}
	if id := selected(r.end(t, false)); id != 2 {
		t.Fatalf("selected %d after a small swipe", id)
	}
	if id := selected(r.key(t, "l", 0)[0]); id != 3 {
		t.Fatalf("selected %d after a small swipe and l, want 3", id)
	}
}

// A four-finger swipe up opens the overview and down closes it on the
// selection, whatever natural-scroll says (as niri). A short one or one
// sideways does nothing.
func TestFourFingerSwipeOverview(t *testing.T) {
	for _, natural := range []bool{false, true} {
		r := startSwipe(t, func(c *ports.Config) { c.Touchpad.NaturalScroll = natural })
		threeColumns(t, r)
		four := func(dx, dy float64, n int) ports.Scene {
			r.at += time.Second
			r.input <- ports.SwipeBegin{Fingers: 4, Time: r.at}
			for range n {
				r.at += 8 * time.Millisecond
				r.input <- ports.SwipeUpdate{DX: dx, DY: dy, Time: r.at}
			}
			return r.end(t, false)
		}
		inOverview := func(s ports.Scene) bool {
			for _, w := range s.Windows {
				if w.Preview > 0 {
					return true
				}
			}
			return false
		}
		if inOverview(four(0, -5, 4)) {
			t.Fatalf("natural=%v: short swipe opened the overview", natural)
		}
		if inOverview(four(-40, 0, 10)) {
			t.Fatalf("natural=%v: sideways swipe opened the overview", natural)
		}
		if !inOverview(four(0, -40, 10)) {
			t.Fatalf("natural=%v: swipe up did not open the overview", natural)
		}
		if !inOverview(four(0, -40, 10)) {
			t.Fatalf("natural=%v: a second swipe up closed it", natural)
		}
		r.key(t, "h", 0)
		s := four(0, 40, 10)
		if inOverview(s) {
			t.Fatalf("natural=%v: swipe down did not close the overview", natural)
		}
		if id := r.focused(t); id != 2 {
			t.Fatalf("natural=%v: closed on %d, want the selection 2", natural, id)
		}
	}
}

// four lifts a four-finger swipe of n updates of (dx, dy) after the
// fingers went down, and returns the scene right after the lift.
func (r *swipeRig) four(t *testing.T, dx, dy float64, n int) ports.Scene {
	t.Helper()
	r.at += time.Second
	r.input <- ports.SwipeBegin{Fingers: 4, Time: r.at}
	for range n {
		r.at += 8 * time.Millisecond
		r.input <- ports.SwipeUpdate{DX: dx, DY: dy, Time: r.at}
	}
	return r.end(t, false)
}

// stashedThird maps three windows and stashes the third (shown, focused),
// settled.
func stashedThird(t *testing.T, r *swipeRig) ports.Scene {
	t.Helper()
	threeColumns(t, r)
	s := r.key(t, "s", ports.ModAlt|ports.ModShift)[0]
	if r.cfg.Animations.On {
		s = r.settle(t)
	}
	return s
}

// A four-finger swipe down with the overview closed hides the stash, and
// the next one shows it again with its windows fading in; the stash's
// windows stay where the swipe found them otherwise.
func TestFourFingerSwipeDownTogglesStash(t *testing.T) {
	r := startSwipe(t, nil)
	if !r.cfg.Animations.On {
		t.Fatal("animations are off in the rig")
	}
	if w := windowOf(t, stashedThird(t, r), 3); w.Hidden {
		t.Fatalf("setup: the stash is hidden: %+v", w)
	}
	r.four(t, 0, 40, 10)
	if w := windowOf(t, r.frame(t, 15*time.Millisecond), 3); w.Hidden || !(w.Fade > 0 && w.Fade < 1) {
		t.Fatalf("hiding: window 3 hidden %t, fade %v, want it leaving", w.Hidden, w.Fade)
	}
	if w := windowOf(t, r.settle(t), 3); !w.Hidden {
		t.Fatalf("swipe down left the stash shown: %+v", w)
	}
	r.four(t, 0, 40, 10)
	if w := windowOf(t, r.frame(t, 15*time.Millisecond), 3); w.Hidden || !(w.Fade > 0 && w.Fade < 1) {
		t.Fatalf("showing: window 3 hidden %t, fade %v, want it appearing", w.Hidden, w.Fade)
	}
	if w := windowOf(t, r.settle(t), 3); w.Hidden || w.Fade != 0 {
		t.Fatalf("second swipe down did not show the stash: %+v", w)
	}
}

// A four-finger swipe up hides the shown stash, with the same fade as a
// swipe down, instead of opening the overview.
func TestFourFingerSwipeUpHidesShownStash(t *testing.T) {
	r := startSwipe(t, nil)
	stashedThird(t, r)
	s := r.four(t, 0, -40, 10)
	for _, w := range s.Windows {
		if w.Preview > 0 {
			t.Fatal("swipe up opened the overview with the stash shown")
		}
	}
	if w := windowOf(t, r.frame(t, 15*time.Millisecond), 3); w.Hidden || !(w.Fade > 0 && w.Fade < 1) {
		t.Fatalf("hiding: window 3 hidden %t, fade %v, want it leaving", w.Hidden, w.Fade)
	}
	s = r.settle(t)
	if w := windowOf(t, s, 3); !w.Hidden {
		t.Fatalf("swipe up left the stash shown: %+v", w)
	}
	for _, w := range s.Windows {
		if w.Preview > 0 {
			t.Fatal("overview open after the stash hid")
		}
	}
}

// With the stash hidden or empty a swipe up opens the overview.
func TestFourFingerSwipeUpOpensOverviewWithoutShownStash(t *testing.T) {
	inOverview := func(s ports.Scene) bool {
		for _, w := range s.Windows {
			if w.Preview > 0 {
				return true
			}
		}
		return false
	}
	for _, name := range []string{"hidden", "empty"} {
		r := startSwipe(t, nil)
		if name == "hidden" {
			stashedThird(t, r)
			r.key(t, "s", ports.ModAlt)
			r.settle(t)
		} else {
			threeColumns(t, r)
		}
		r.four(t, 0, -40, 10)
		if !inOverview(r.settle(t)) {
			t.Fatalf("%s stash: swipe up did not open the overview", name)
		}
	}
}

// The stash shown over a covering fullscreen window is on screen: a swipe
// up hides it and leaves the window fullscreen.
func TestFourFingerSwipeUpHidesStashOverCover(t *testing.T) {
	r := startSwipe(t, nil)
	stashedThird(t, r)
	r.key(t, "s", ports.ModAlt) // hide
	r.settle(t)
	r.key(t, "f", ports.ModAlt|ports.ModShift)
	r.settleAll(t, r.outs...)
	shown := r.key(t, "s", ports.ModAlt)[0] // show over the cover
	sc, _ := r.settleAll(t, r.outs...)
	if len(sc) == 0 {
		sc = []ports.Scene{shown}
	}
	if w := windowOf(t, sc[0], 3); w.Hidden {
		t.Fatalf("setup: the stash is hidden over the cover: %+v", w)
	}
	s := r.four(t, 0, -40, 10)
	for _, w := range s.Windows {
		if w.Preview > 0 {
			t.Fatal("swipe up opened the overview with the stash over the cover")
		}
	}
	// Over a cover the stash hides without a fade: the scene is final.
	if w := windowOf(t, s, 3); !w.Hidden {
		t.Fatalf("swipe up left the stash over the cover: %+v", w)
	}
	if w := windowOf(t, s, 2); !w.Fullscreen {
		t.Fatalf("the covering window left fullscreen: %+v", w)
	}
}

// With nothing in the stash a swipe down changes nothing: the scene is the
// same one, with no new Seq and no animation.
func TestFourFingerSwipeDownEmptyStash(t *testing.T) {
	r := startSwipe(t, nil)
	before := threeColumns(t, r)
	after := r.four(t, 0, 40, 10)
	if after.Seq != before.Seq || !after.SameAs(before) {
		t.Fatalf("empty stash: scene changed, seq %d then %d", before.Seq, after.Seq)
	}
	if _, ok := r.settleAll(t, r.outs...); ok {
		t.Fatal("empty stash: the swipe started an animation")
	}
}

// With the overview open a swipe down closes it and does not touch the
// stash, shown or hidden.
func TestFourFingerSwipeDownInOverviewKeepsStash(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		r := startSwipe(t, nil)
		stashedThird(t, r)
		if hidden {
			r.key(t, "s", ports.ModAlt)
			r.settle(t)
		}
		r.four(t, 0, -40, 10)
		r.settle(t)
		s := r.four(t, 0, 40, 10)
		for _, w := range s.Windows {
			if w.Preview > 0 {
				t.Fatalf("hidden=%v: swipe down left the overview open", hidden)
			}
		}
		if w := windowOf(t, r.settle(t), 3); w.Hidden != hidden {
			t.Fatalf("hidden=%v: window 3 hidden %t after the overview closed", hidden, w.Hidden)
		}
	}
}

// Opening the overview during a column swipe drops the swipe: the rest
// of it neither slides the columns under the overview nor lands them,
// also once the overview closed again (escape before the rest).
func TestOverviewOpenedMidSwipeDropsIt(t *testing.T) {
	for _, closeFirst := range []bool{false, true} {
		r := startSwipe(t, nil)
		settled := threeColumns(t, r)
		r.begin()
		r.move(t, -40, 0)
		r.move(t, -40, 0)
		r.key(t, "o", ports.ModAlt)
		// s is the latest scene.
		var s ports.Scene
		if closeFirst {
			s = r.key(t, "Escape", 0)[0]
		}
		for range 20 {
			r.at += 8 * time.Millisecond
			r.input <- ports.SwipeUpdate{DX: -80, Time: r.at}
		}
		// The updates publish nothing (the overview gesture, or a dropped
		// swipe); the lift always publishes, so its scene is received
		// before the next event: core is idle when the escape and the
		// flips arrive, each waits for its own scene.
		r.input <- ports.SwipeEnd{Time: r.at}
		s = scene(t, r.scenes)
		if !closeFirst {
			s = r.key(t, "Escape", 0)[0]
		}
		// Past any spring: the escape's own springs (the cards zooming
		// back) may chain after the swipe's landing, so settle until
		// none runs.
		for range 4 {
			set, ok := r.settleAll(t, wide.Name)
			if !ok {
				break
			}
			s = set[0]
		}
		if len(s.Windows) == 0 || slices.ContainsFunc(s.Windows, func(w ports.SceneWindow) bool {
			// Neither a card nor a window still zooming back.
			return w.Preview > 0 || w.Zoom > 0 || w.Fade > 0
		}) {
			t.Fatalf("closeFirst=%v: the scene never settled: %+v", closeFirst, s.Windows)
		}
		for _, id := range []ports.WindowID{1, 2, 3} {
			before, _ := rectOf(settled, id)
			if after, _ := rectOf(s, id); before != after {
				t.Fatalf("closeFirst=%v: window %d moved: %+v then %+v", closeFirst, id, before, after)
			}
		}
	}
}

// A hard flick down from the first of four workspaces lands on the second:
// one swipe never skips a workspace.
func TestSwipeHardFlickMovesOneWorkspace(t *testing.T) {
	r := startSwipe(t, nil)
	for id := ports.WindowID(1); id <= 4; id++ {
		r.mapWindow(t, id)
		r.key(t, "Next", ports.ModAlt)
	}
	for range 4 {
		r.key(t, "Prior", ports.ModAlt)
	}
	r.flick(t, 20, 0, 60)
	s := r.settle(t)
	if got, ok := rectOf(s, 2); !ok || got.Y != 0 {
		t.Fatalf("hard flick did not land on workspace 2: %v %t", got, ok)
	}
}

// A hard flick across many columns moves the view one column edge.
func TestSwipeHardFlickMovesOneColumn(t *testing.T) {
	r := startSwipe(t, nil)
	for id := ports.WindowID(1); id <= 5; id++ {
		r.mapWindow(t, id)
	}
	// The view shows 4 and 5; a hard flick left shows 3 and 4, not 1.
	r.flick(t, 20, -60, 0)
	s := r.settle(t)
	if got, ok := rectOf(s, 3); !ok || got.X != 0 {
		t.Fatalf("column 3 at %v %t, want the left edge", got, ok)
	}
}

// A two-finger scroll that steps in the overview and ends with the pointer
// on another output does not hold the next scroll back.
func TestOverviewScrollStopOnOtherOutput(t *testing.T) {
	r := startSwipe(t, nil)
	threeColumns(t, r)
	r.plug(t, ports.OutputInfo{Name: "DP-2", Width: 800, Height: 600, RefreshMilli: 60000})
	r.key(t, "o", ports.ModAlt)
	selected := func(s ports.Scene) ports.WindowID {
		for _, w := range s.Windows {
			if w.Focused && w.Preview > 0 {
				return w.ID
			}
		}
		return 0
	}
	on := func(id ports.WindowID) func(ports.Scene) bool {
		return func(s ports.Scene) bool { return s.Output == wide.Name && selected(s) == id }
	}
	finger := func(dx float64) ports.PointerAxis {
		return ports.PointerAxis{Source: ports.AxisFinger, Horizontal: ports.ScrollAxis{Set: true, Value: dx}}
	}
	r.input <- ports.PointerMotion{X: 100, Y: 300}
	r.input <- finger(-70)
	sceneMatch(t, r.scenes, on(2))
	// The pointer crosses to DP-2 and the fingers lift there.
	r.input <- ports.PointerMotion{X: 1000, Y: 300}
	r.input <- ports.PointerAxis{Source: ports.AxisFinger, Horizontal: ports.ScrollAxis{Set: true, Stop: true}}
	r.input <- ports.PointerMotion{X: 100, Y: 300}
	r.input <- finger(-70)
	sceneMatch(t, r.scenes, on(1))
}

// The output shrinks mid-swipe: the snap points no longer hold, so the
// swipe lets go. The view shows what the resize alone shows, and the rest
// of the swipe moves nothing.
func TestSwipeOutputResizedMidSwipe(t *testing.T) {
	smaller := ports.OutputInfo{Name: wide.Name, Width: 600, Height: 600, RefreshMilli: 60000}
	four := func(r *swipeRig) {
		for id := ports.WindowID(1); id <= 4; id++ {
			r.mapWindow(t, id)
		}
	}
	plain := startSwipe(t, nil)
	four(plain)
	plain.output <- ports.OutputAdded{Info: smaller}
	want := scene(t, plain.scenes)

	r := startSwipe(t, nil)
	four(r)
	r.begin()
	for range 3 {
		r.move(t, -40, 0)
	}
	r.output <- ports.OutputAdded{Info: smaller}
	scene(t, r.scenes)
	// The first update after the resize lets go: the slide stops.
	s := r.move(t, -40, 0)
	for _, id := range []ports.WindowID{1, 2, 3, 4} {
		w, _ := rectOf(want, id)
		if got, _ := rectOf(s, id); got != w {
			t.Fatalf("window %d at %+v after the next update, want %+v", id, got, w)
		}
	}
	for range 2 {
		r.at += 8 * time.Millisecond
		r.input <- ports.SwipeUpdate{DX: -40, Time: r.at}
	}
	s = r.end(t, false)
	for _, id := range []ports.WindowID{1, 2, 3, 4} {
		w, _ := rectOf(want, id)
		if got, _ := rectOf(s, id); got != w {
			t.Fatalf("window %d at %+v, want %+v as without the swipe", id, got, w)
		}
	}
}

// A window maps mid-swipe: the column snap points moved, so the swipe lets
// go and the view shows what the map alone shows.
func TestSwipeWindowMappedMidSwipe(t *testing.T) {
	plain := startSwipe(t, nil)
	threeColumns(t, plain)
	want := plain.mapWindow(t, 4)[0]

	r := startSwipe(t, nil)
	threeColumns(t, r)
	r.begin()
	for range 3 {
		r.move(t, -40, 0)
	}
	r.mapWindow(t, 4)
	r.move(t, -40, 0)
	r.at += 8 * time.Millisecond
	r.input <- ports.SwipeUpdate{DX: -40, Time: r.at}
	s := r.end(t, false)
	for _, id := range []ports.WindowID{1, 2, 3, 4} {
		w, _ := rectOf(want, id)
		if got, _ := rectOf(s, id); got != w {
			t.Fatalf("window %d at %+v, want %+v as without the swipe", id, got, w)
		}
	}
}

// A press that moves the view during a landing slide redirects the slide
// without stopping or reversing it: the view keeps its speed.
func TestSwipeRetargetKeepsVelocity(t *testing.T) {
	r := startSwipe(t, nil)
	for id := ports.WindowID(1); id <= 4; id++ {
		r.mapWindow(t, id)
	}
	// Focus on 4, the view shows 3 and 4. Go back to the first column.
	for range 3 {
		r.key(t, "Left", ports.ModAlt)
	}
	r.settle(t)
	// A flick toward the right lands on a later column: the view moves
	// right, so column 1 moves left.
	r.flick(t, 4, 40, 0)
	mid := r.frame(t, 16*time.Millisecond)
	prev, ok := rectOf(mid, 1)
	if !ok {
		t.Fatal("column 1 hidden during the landing")
	}
	if prev.X >= 0 {
		t.Fatalf("view did not move right: column 1 at %d", prev.X)
	}
	r.key(t, "Right", ports.ModAlt)
	// The retargeted spring keeps the speed of the landing: the view moves
	// on from the first frame and never goes back.
	for i := range 2 {
		s := r.frame(t, 16*time.Millisecond)
		got, _ := rectOf(s, 1)
		if got.X > prev.X || (i == 1 && got.X == prev.X) {
			t.Fatalf("frame %d: column 1 at %d after %d, the view reversed or stopped", i, got.X, prev.X)
		}
		prev = got
	}
}
