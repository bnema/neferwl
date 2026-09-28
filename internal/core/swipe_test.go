package core_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/core"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

// swipeRig runs core on one 800x600 output with no gaps or border, a
// clock the test moves, and page flips the test sends.
type swipeRig struct {
	*multiRig
	frames chan ports.OutputFrame
	mu     sync.Mutex
	now    time.Time
	// at is the device time of the next swipe event.
	at time.Duration
}

var wide = ports.OutputInfo{Name: "DP-1", Width: 800, Height: 600, RefreshMilli: 60000}

func startSwipe(t *testing.T, edit func(*ports.Config)) *swipeRig {
	t.Helper()
	cfg := config.Defaults()
	cfg.Keyboard.CmdKey = "alt"
	cfg.Border.Width = 0
	cfg.Layout.Gaps = 0
	cfg.Terminal.AutoOpen = "never"
	if edit != nil {
		edit(&cfg)
	}
	r := &swipeRig{frames: make(chan ports.OutputFrame), now: time.Unix(100, 0)}
	r.multiRig = &multiRig{
		client: make(chan ports.ClientEvent, 16), input: make(chan ports.InputEvent, 64),
		output: make(chan ports.OutputEvent, 4), reload: make(chan ports.ConfigChanged, 4),
		commands: make(chan ports.ClientCommand, 1024), scenes: make(chan []ports.Scene, 1), spawn: make(chan ports.SpawnRequest, 16), state: make(chan ports.State, 1), cfg: cfg,
	}
	clock := portsmocks.NewMockClock(t)
	clock.EXPECT().Now().RunAndReturn(func() time.Time {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.now
	}).Maybe()
	// The fallback timer never fires: frames come from the test.
	clock.EXPECT().NewTimer(mock.Anything).RunAndReturn(func(time.Duration) ports.Timer {
		timer := portsmocks.NewMockTimer(t)
		timer.EXPECT().C().Return(make(chan time.Time)).Maybe()
		timer.EXPECT().Stop().Return(true).Maybe()
		return timer
	}).Maybe()
	c, err := core.New(cfg, core.Channels{Client: r.client, Input: r.input, Output: r.output, Config: r.reload, Commands: r.commands, Scenes: r.scenes, Spawn: r.spawn, State: r.state, Clock: clock, Frames: r.frames})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	r.plug(t, wide)
	return r
}

func (r *swipeRig) advance(d time.Duration) {
	r.mu.Lock()
	r.now = r.now.Add(d)
	r.mu.Unlock()
}

// begin starts a swipe; nothing moves until it picks an axis.
func (r *swipeRig) begin() {
	r.at += time.Second
	r.input <- ports.SwipeBegin{Time: r.at}
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
	r.key(t, "Next", ports.ModAlt)
	r.mapWindow(t, 2)
	r.key(t, "Prior", ports.ModAlt)
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
	// 10 units of 1200 per 800 px view: 7 px, not snapped to a column.
	if d := after.X - before.X; d != 7 {
		t.Fatalf("column moved %d px with the fingers", d)
	}
	s = r.move(t, -30, 0)
	if got, _ := rectOf(s, 2); got.X-before.X != 27 {
		t.Fatalf("column moved %d px after 40 units", got.X-before.X)
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
	r.key(t, "Next", ports.ModAlt)
	r.key(t, "Next", ports.ModAlt)
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
	r.key(t, "Next", ports.ModAlt)
	s := r.move(t, 0, 30)
	if got, ok := rectOf(s, 2); !ok || got.Y != 0 {
		t.Fatalf("workspace 2 at %v %t after the key", got, ok)
	}
	if _, ok := rectOf(s, 1); ok {
		t.Fatal("workspace 1 still sliding")
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
	// Core sends the pointer update right after the scene: wait for it.
	for {
		var v ports.ClientCommand
		select {
		case v = <-r.commands:
		case <-time.After(50 * time.Millisecond):
		}
		if v == nil {
			break
		}
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

func TestSecondSwipeBeginSettlesTheFirst(t *testing.T) {
	r := startSwipe(t, nil)
	threeColumns(t, r)
	r.begin()
	s := r.move(t, -40, 0)
	moved, _ := rectOf(s, 2)
	// Another touchpad begins: the first swipe springs back.
	r.begin()
	scene(t, r.scenes)
	s = r.settle(t)
	if got, _ := rectOf(s, 2); got.X == moved.X || got.X != 0 {
		t.Fatalf("first swipe left column 2 at %d", got.X)
	}
}

// drain waits for core to handle what was sent, then empties commands.
func (r *swipeRig) drain() {
	for {
		select {
		case <-r.commands:
		case <-time.After(50 * time.Millisecond):
			return
		}
	}
}

func TestKeyWithoutSlideMovesAtOnce(t *testing.T) {
	r := startSwipe(t, nil)
	threeColumns(t, r)
	r.key(t, "Left", ports.ModAlt)
	s := r.key(t, "Left", ports.ModAlt)[0]
	if got, _ := rectOf(s, 1); got.X != 0 {
		t.Fatalf("column 1 at %d", got.X)
	}
}
