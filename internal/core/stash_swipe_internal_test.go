package core

import (
	"math"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

// stashSwipe drives a three-finger swipe on the stash of stashRig (windows
// 4, 3, 2; 2 selected at the right end).
type stashSwipe struct {
	t  *testing.T
	c  *Core
	ic *indicatorClock
	at time.Duration
}

func newStashSwipe(t *testing.T, edit func(*Core)) (*stashSwipe, *Workspace) {
	t.Helper()
	c, ic, sc, _ := stashRig(t, true)
	if edit != nil {
		edit(c)
	}
	return &stashSwipe{t: t, c: c, ic: ic}, sc.mon.Current()
}

func (s *stashSwipe) begin() {
	s.at += time.Second
	s.c.swipeBegin(ports.SwipeBegin{Fingers: 3, Time: s.at})
}

func (s *stashSwipe) move(dx float64) {
	s.at += 8 * time.Millisecond
	s.c.swipeUpdate(ports.SwipeUpdate{DX: dx, Time: s.at})
}

func (s *stashSwipe) end(cancelled bool) {
	s.c.swipeEnd(ports.SwipeEnd{Cancelled: cancelled, Time: s.at})
}

// flick is a quick swipe of n updates of dx, lifted at once.
func (s *stashSwipe) flick(n int, dx float64) {
	s.begin()
	for range n {
		s.move(dx)
	}
	s.end(false)
}

// The selection is the right end of 4, 3, 2. Fingers moving right bring the
// left neighbor in (natural scroll off: the content follows the fingers
// opposite, so sideways left reaches the left neighbor like focus left).

// The view follows the fingers: halfway to the neighbor it has moved half a
// window, its veil half, and nothing is selected yet.
func TestStashSwipeFollowsFingers(t *testing.T) {
	s, w := newStashSwipe(t, nil)
	s.begin()
	s.move(-10) // under the decision threshold
	if w.stashView.off != 0 {
		t.Fatalf("moved %v before picking an axis", w.stashView.off)
	}
	s.move(-20)
	if g := s.c.swipe; g == nil || g.mode != swipeStash {
		t.Fatalf("swipe %+v, want a stash swipe", g)
	}
	before := w.stashView.off
	s.move(-40)
	if !(w.stashView.off < before) || w.stashAt != 2 {
		t.Fatalf("offset %v then %v, stashAt %d: the view must follow and the selection stay", before, w.stashView.off, w.stashAt)
	}
	sc := sceneWindow(t, indicatorScene(t, s.c), 2)
	if sc.Dim <= 0 || sc.Dim >= 0.5 {
		t.Fatalf("selected window veil %v mid-swipe, want strictly between 0 and the stash dim", sc.Dim)
	}
}

// A flick of any length lands on the neighbor at most: no window is skipped.
func TestStashSwipeNeverSkipsAWindow(t *testing.T) {
	for _, n := range []int{3, 10, 40, 200} {
		s, w := newStashSwipe(t, nil)
		s.flick(n, -80)
		if w.stashAt != 1 {
			t.Fatalf("%d updates: stashAt %d, want 1 (one window at most)", n, w.stashAt)
		}
		if !w.stashFocus {
			t.Fatalf("%d updates: the stash lost the focus", n)
		}
		// The view itself never went past the neighbor either.
		if view := float64(w.stashAt) + w.stashView.off; view < 1-0.1 {
			t.Fatalf("%d updates: the view is at %v, past the neighbor at 1", n, view)
		}
	}
}

// A slow short swipe settles back where it began.
func TestStashSwipeShortSwipeReturns(t *testing.T) {
	s, w := newStashSwipe(t, nil)
	s.begin()
	s.move(-20)
	s.at += 2 * time.Second
	s.move(-30)
	s.at += 2 * time.Second
	s.end(false)
	if w.stashAt != 2 {
		t.Fatalf("stashAt %d after a slow short swipe, want 2", w.stashAt)
	}
	s.ic.now = s.ic.now.Add(5 * time.Second)
	s.c.animate(s.ic.now, nil)
	if w.stashView.off != 0 || w.stashView.motion.on {
		t.Fatalf("offset %v, spring %v: want settled", w.stashView.off, w.stashView.motion.on)
	}
}

// A cancelled swipe returns to the selection.
func TestStashSwipeCancelReturns(t *testing.T) {
	s, w := newStashSwipe(t, nil)
	s.begin()
	for range 20 {
		s.move(-40)
	}
	s.end(true)
	if w.stashAt != 2 {
		t.Fatalf("stashAt %d after a cancel", w.stashAt)
	}
}

// Past the first or last window the view only stretches: the end of the
// stash holds the selection.
func TestStashSwipeEdgeHolds(t *testing.T) {
	s, w := newStashSwipe(t, nil)
	s.flick(20, 80) // right: past the last window
	if w.stashAt != 2 {
		t.Fatalf("stashAt %d past the end, want 2", w.stashAt)
	}
	s.flick(20, -80)
	s.flick(20, -80)
	s.ic.now = s.ic.now.Add(5 * time.Second)
	s.c.animate(s.ic.now, nil)
	s.flick(20, -80) // 4 is first now: past the start
	if w.stashAt != 0 {
		t.Fatalf("stashAt %d, want 0 at the start", w.stashAt)
	}
}

// Natural scroll moves the view the other way, as for columns.
func TestStashSwipeNaturalScrollInverts(t *testing.T) {
	s, w := newStashSwipe(t, func(c *Core) { c.cfg.Touchpad.NaturalScroll = true })
	s.flick(10, 80)
	if w.stashAt != 1 {
		t.Fatalf("stashAt %d with natural scroll, want 1", w.stashAt)
	}
}

// With animations off a swipe takes the discrete path (nothing slides) and
// still steps one window.
func TestStashSwipeAnimationsOffStepsOnce(t *testing.T) {
	s, w := newStashSwipe(t, func(c *Core) { c.cfg.Animations.On = false })
	s.flick(40, -80)
	if w.stashAt != 1 || w.stashView.off != 0 {
		t.Fatalf("stashAt %d, offset %v with animations off", w.stashAt, w.stashView.off)
	}
}

// A bind during the swipe lets go of it: the rest does nothing.
func TestStashSwipeDropsWhenSelectionChanges(t *testing.T) {
	s, w := newStashSwipe(t, nil)
	s.begin()
	s.move(-40)
	s.move(-40)
	s.c.applyAction(ActionFocusColumnLeft)
	s.move(-40)
	if g := s.c.swipe; g.mode != swipeDropped {
		t.Fatalf("mode %v, want dropped", g.mode)
	}
	at := w.stashAt
	s.end(false)
	if w.stashAt != at || w.stashView.off != 0 {
		t.Fatalf("stashAt %d then %d, offset %v: the dropped swipe moved the stash", at, w.stashAt, w.stashView.off)
	}
}

// Fingers on the stash take a running landing slide where it is.
func TestStashSwipeCatchesLandingSlide(t *testing.T) {
	s, w := newStashSwipe(t, nil)
	s.flick(10, -80)
	s.ic.now = s.ic.now.Add(30 * time.Millisecond)
	s.c.animate(s.ic.now, nil)
	mid := w.stashView.off
	if mid == 0 || !w.stashView.motion.on {
		t.Fatalf("setup: offset %v, spring %v", mid, w.stashView.motion.on)
	}
	s.begin()
	s.move(-20)
	s.move(-20)
	if w.stashView.motion.on {
		t.Fatal("the landing spring still runs under the fingers")
	}
}

// The stash never brings another workspace on screen: its landing must not
// report a shown workspace, which would respawn the empty slots of a named
// one.
func TestStashSwipeLandingShowsNoWorkspace(t *testing.T) {
	s, w := newStashSwipe(t, nil)
	s.begin()
	for range 10 {
		s.move(-80)
	}
	if shown := s.c.swipeEnd(ports.SwipeEnd{Time: s.at}); shown {
		t.Fatal("a stash landing reported a workspace shown")
	}
	if w.stashAt != 1 {
		t.Fatalf("stashAt %d, want the landing on 1", w.stashAt)
	}
}

// A native float with the focus over the stash keeps the swipe discrete: the
// stash must not slide behind it and snap back. One that takes the focus
// mid-swipe lets go of the stash.
func TestStashSwipeUnderFocusedFloatIsDiscrete(t *testing.T) {
	s, w := newStashSwipe(t, nil)
	w.AddFloating(9, 100, 80)
	if !w.floatFocus {
		t.Fatal("setup: the float has no focus")
	}
	s.begin()
	s.move(-40)
	if g := s.c.swipe; g.mode != swipeDiscrete {
		t.Fatalf("mode %v, want discrete", g.mode)
	}
	if w.stashView.off != 0 {
		t.Fatalf("stash slid by %v under a focused float", w.stashView.off)
	}
}

func TestStashSwipeDropsWhenAFloatTakesTheFocus(t *testing.T) {
	s, w := newStashSwipe(t, nil)
	s.begin()
	s.move(-40)
	s.move(-40)
	w.AddFloating(9, 100, 80)
	s.move(-40)
	if g := s.c.swipe; g.mode != swipeDropped || w.stashView.off != 0 {
		t.Fatalf("mode %v, offset %v: want dropped and settled", g.mode, w.stashView.off)
	}
}

// A session lock lets go of the swipe and settles the stash it moved.
func TestStashSwipeSecurityLockSettlesTheStash(t *testing.T) {
	s, w := newStashSwipe(t, nil)
	s.begin()
	s.move(-40)
	s.move(-40)
	if w.stashView.off == 0 {
		t.Fatal("setup: the stash did not follow")
	}
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().Return(ports.SecurityState{Generation: 1, Protected: true})
	s.c.opts.Security = gate
	if !s.c.syncSecurity() {
		t.Fatal("the lock was not taken")
	}
	if s.c.swipe != nil || w.stashView.off != 0 {
		t.Fatalf("swipe %v, offset %v after the lock", s.c.swipe, w.stashView.off)
	}
}

// With the focus on another output when the fingers lift, the selection
// stays and the view springs back.
func TestStashSwipeLiftOnAnotherOutputSpringsBack(t *testing.T) {
	s, w := newStashSwipe(t, nil)
	s.c.addScreen(ports.OutputInfo{Name: "B", Width: 300, Height: 200})
	s.begin()
	s.move(-40)
	s.move(-80)
	s.move(-80)
	s.c.focusScreen = 1
	s.end(false)
	if w.stashAt != 2 {
		t.Fatalf("stashAt %d, want the selection kept at 2", w.stashAt)
	}
	if !w.stashView.motion.on {
		t.Fatal("the view does not spring back")
	}
	s.ic.now = s.ic.now.Add(5 * time.Second)
	s.c.animate(s.ic.now, nil)
	if w.stashView.off != 0 {
		t.Fatalf("offset %v after the spring", w.stashView.off)
	}
}

// A swipe whose screen is gone lets go of the stash it shifted.
func TestStashSwipeLosingItsScreenSettlesTheStash(t *testing.T) {
	for _, onEnd := range []bool{false, true} {
		s, w := newStashSwipe(t, nil)
		s.begin()
		s.move(-40)
		s.move(-40)
		if w.stashView.off == 0 {
			t.Fatal("setup: the stash did not follow")
		}
		s.c.swipe.screen = &screen{} // its output unplugged
		if onEnd {
			s.end(false)
		} else {
			s.move(-40)
		}
		if w.stashView.off != 0 || s.c.swipe != nil {
			t.Fatalf("end=%v: offset %v, swipe %v: want settled and gone", onEnd, w.stashView.off, s.c.swipe)
		}
	}
}

// The overview shows the settled state: a stash landing spring stops.
func TestOverviewStopsStashSlide(t *testing.T) {
	s, w := newStashSwipe(t, nil)
	s.flick(10, -80)
	if !w.stashView.motion.on {
		t.Fatal("setup: no landing spring")
	}
	s.c.cur().mon.ToggleOverview()
	if w.stashView.motion.on || w.stashView.off != 0 {
		t.Fatalf("spring %v, offset %v under the overview", w.stashView.motion.on, w.stashView.off)
	}
}

// Quick chained navigations keep the view where it is drawn, however far it
// is from the selection: nothing jumps, and the slide settles on the last.
func TestStashNavigationChainsManyWithoutJump(t *testing.T) {
	c, ic, sc, _ := stashRig(t, true)
	w := sc.mon.Current()
	for id := WindowID(5); id <= 8; id++ {
		sc.mon.AddWindow(id)
		w.FocusID(id)
		c.applyAction(ActionToggleWindowStash)
	}
	settleShown(t, c)
	if len(w.Stash) != 7 {
		t.Fatalf("setup: %d stashed", len(w.Stash))
	}
	prev := float64(w.stashAt) + w.stashView.off
	for i := range 5 {
		act(c, ic, ActionFocusColumnLeft)
		pos := float64(w.stashAt) + w.stashView.off
		if pos < prev-1e-6 || pos > prev+1e-6 {
			t.Fatalf("navigation %d: the drawn view jumped from %v to %v", i, prev, pos)
		}
		ic.now = ic.now.Add(10 * time.Millisecond)
		c.animate(ic.now, nil)
		prev = float64(w.stashAt) + w.stashView.off
	}
	// The view is still several windows from the selection: the window it
	// is on is drawn there, however far it is from stashAt.
	at := int(math.Round(float64(w.stashAt) + w.stashView.off))
	if at-w.stashAt < 3 {
		t.Fatalf("setup: the view is %d windows from the selection", at-w.stashAt)
	}
	near := w.Stash[at].ID
	if got := sceneWindow(t, indicatorScene(t, c), near); got.Hidden || got.Rect.W == 0 {
		t.Fatalf("window %d, %d away from the selection, is not drawn under the view: %+v", near, at-w.stashAt, got)
	}
	ic.now = ic.now.Add(5 * time.Second)
	c.animate(ic.now, nil)
	if w.stashView.off != 0 || w.stashView.motion.on {
		t.Fatalf("offset %v, spring %v: want settled", w.stashView.off, w.stashView.motion.on)
	}
	if got := sceneWindow(t, indicatorScene(t, c), near); !got.Hidden {
		t.Fatalf("window %d still drawn after the settle: %+v", near, got)
	}
}

// A captured workspace caught mid-slide draws the veil the screen does:
// a share of Stash.Dim, not the full one and not none.
func TestCaptureSceneDrawsTheSlidingVeil(t *testing.T) {
	s, w := newStashSwipe(t, nil)
	s.c.cfg.Stash.Dim = 0.5
	s.begin()
	s.move(-40)
	s.move(-80)
	sc := s.c.screens[0]
	s.c.configures.cw.load(sc, w)
	scene := s.c.captureScene(1)
	if scene == nil {
		t.Fatal("no capture scene")
	}
	var partial bool
	for _, p := range s.c.configures.cw.placements {
		if p.Veil <= 0 || p.Veil >= 1 {
			continue
		}
		partial = true
		got := sceneWindow(t, *scene, p.ID).Dim
		if want := s.c.cfg.Stash.Dim * p.Veil; math.Abs(got-want) > 1e-9 {
			t.Fatalf("window %d captured with dim %v, want %v", p.ID, got, want)
		}
	}
	if !partial {
		t.Fatal("setup: no window has a partial veil")
	}
}

// An output unplugged under the swipe: the host adopts its workspaces, and
// the stash they carry must not stay shifted.
func TestStashSwipeUnpluggedOutputSettlesTheAdoptedStash(t *testing.T) {
	s, w := newStashSwipe(t, nil)
	s.c.addScreen(ports.OutputInfo{Name: "B", Width: 300, Height: 200})
	s.begin()
	s.move(-40)
	s.move(-80)
	if w.stashView.off == 0 {
		t.Fatal("setup: the stash did not follow")
	}
	s.c.removeScreen("A")
	if s.c.swipe != nil || w.stashView.off != 0 {
		t.Fatalf("swipe %v, offset %v after the unplug", s.c.swipe, w.stashView.off)
	}
}
