package core

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// stashRig is indicatorCore with animations on and a stash of windows 4, 3
// and 2 (2 selected, 3 peeking on its left) over the tile 1, settled. The
// peek veil is 0.5.
func stashRig(t *testing.T, on bool) (*Core, *indicatorClock, *screen, chan ports.ClientCommand) {
	t.Helper()
	c, ic, cmds := fadeCore(t)
	c.cfg.Animations.On = on
	c.cfg.Stash.Dim = 0.5
	sc := c.cur()
	for id := WindowID(1); id <= 4; id++ {
		sc.mon.AddWindow(id)
	}
	for _, id := range []WindowID{4, 3, 2} {
		sc.mon.Current().FocusID(id)
		c.applyAction(ActionToggleWindowStash)
	}
	settleShown(t, c)
	drainConfigures(cmds)
	return c, ic, sc, cmds
}

// act is a bind: snapshot, action, transition.
func act(c *Core, ic *indicatorClock, a Action) {
	shots := c.snapshot(ic.now)
	c.applyAction(a)
	c.transition(shots, ic.now)
}

// Showing the stash fades in and grows every window it brings on screen;
// the windows it keeps off screen stay out; the settled layout is the
// final one at once, with no extra configure.
func TestStashShowAppears(t *testing.T) {
	c, ic, sc, cmds := stashRig(t, true)
	act(c, ic, ActionToggleStashVisible)
	settleShown(t, c)
	if !sc.mon.Current().stashHidden {
		t.Fatal("setup: the stash is shown")
	}
	ic.now = ic.now.Add(5 * time.Second)
	c.animate(ic.now, nil)
	settleShown(t, c)
	drainConfigures(cmds)

	act(c, ic, ActionToggleStashVisible)
	s := indicatorScene(t, c)
	for _, id := range []WindowID{2, 3} {
		p := settledOf(t, sc, id)
		w := sceneWindow(t, s, id)
		if w.Hidden || w.Fade != 1 || w.Rect != scaledRect(p.Rect, appearScale) || w.Zoom < 0.85 || w.Zoom > 0.95 {
			t.Fatalf("window %d first frame %+v, want invisible at 90 %% of %+v", id, w, p.Rect)
		}
	}
	if w := sceneWindow(t, s, 4); !w.Hidden || w.Fade != 0 {
		t.Fatalf("window 4 stays off screen: %+v", w)
	}
	// The action configures what it changes once, at the settled size.
	cfgs := configuresOf(cmds)
	for _, id := range []WindowID{2, 3} {
		if len(cfgs[id]) != 1 {
			t.Fatalf("window %d configured %d times, want once: %+v", id, len(cfgs[id]), cfgs[id])
		}
		want := c.clientRect(settledOf(t, sc, id))
		if v := cfgs[id][0]; !v.Visible || v.Width != want.W || v.Height != want.H {
			t.Fatalf("window %d configure %+v, want %dx%d (the settled client rect)", id, v, want.W, want.H)
		}
	}

	s = frame(t, c, ic, 40*time.Millisecond)
	for _, id := range []WindowID{2, 3} {
		w := sceneWindow(t, s, id)
		if !(w.Fade > 0 && w.Fade < 1) || !(w.Zoom > 0.9 && w.Zoom < 1) {
			t.Fatalf("window %d mid-way fade %v zoom %v, want strictly between", id, w.Fade, w.Zoom)
		}
	}
	s = frame(t, c, ic, 5*time.Second)
	for _, id := range []WindowID{2, 3} {
		p := settledOf(t, sc, id)
		if w := sceneWindow(t, s, id); w.Fade != 0 || w.Zoom != 0 || w.Rect != p.Rect {
			t.Fatalf("window %d settled %+v, want %+v", id, w, p.Rect)
		}
	}
	if c.animating() || len(sc.rects) != 0 {
		t.Fatalf("still animating: %d rects", len(sc.rects))
	}
	if ids := drainConfigures(cmds); len(ids) != 0 {
		t.Fatalf("the animation reconfigured %v", ids)
	}
}

// Hiding the stash keeps its windows drawn while they fade out and shrink,
// out of reach of input and focus, until they settle.
func TestStashHideLeaves(t *testing.T) {
	c, ic, sc, cmds := stashRig(t, true)
	left := map[WindowID]Placement{}
	for _, id := range []WindowID{2, 3} {
		left[id] = settledOf(t, sc, id)
	}
	act(c, ic, ActionToggleStashVisible)
	s := indicatorScene(t, c)
	if len(sc.settledLayout) != len(sc.shown) {
		t.Fatalf("settled %d shown %d", len(sc.settledLayout), len(sc.shown))
	}
	for _, id := range []WindowID{2, 3} {
		w := sceneWindow(t, s, id)
		if w.Hidden || w.Fade != 0 || w.Rect != left[id].Rect {
			t.Fatalf("window %d first frame %+v, want drawn opaque at %+v", id, w, left[id].Rect)
		}
		if _, _, ok := c.windowRect(id); ok {
			t.Fatalf("window %d is still on screen for input", id)
		}
	}
	// The selected window is under the tile: its centre hits the tile.
	r := left[2].Rect
	if id, _, _ := c.hit(float64(r.X+r.W/2), float64(r.Y+r.H/2)); id == 2 || id == 3 {
		t.Fatalf("hit %d on a leaving window", id)
	}
	if id, _ := sc.mon.Focused(); id == 2 || id == 3 {
		t.Fatalf("focused %d", id)
	}
	if ids := drainConfigures(cmds); slices.Contains(ids, 2) || slices.Contains(ids, 3) {
		t.Fatalf("a leaving window was configured: %v", ids)
	}

	s = frame(t, c, ic, 40*time.Millisecond)
	for _, id := range []WindowID{2, 3} {
		if w := sceneWindow(t, s, id); !(w.Fade > 0 && w.Fade < 1) || !(w.Rect.W < left[id].Rect.W) {
			t.Fatalf("window %d mid-way %+v", id, w)
		}
	}
	s = frame(t, c, ic, 5*time.Second)
	for _, id := range []WindowID{2, 3} {
		if w := sceneWindow(t, s, id); !w.Hidden {
			t.Fatalf("window %d still drawn after the settle: %+v", id, w)
		}
	}
	if c.animating() || len(sc.rects) != 0 {
		t.Fatalf("still animating: %d rects", len(sc.rects))
	}
	// The hide's own configure comes once the fade is over, from the size
	// the window had (not from nothing).
	var hidden []ports.ConfigureWindow
	for {
		select {
		case cmd := <-cmds:
			if v, ok := cmd.(ports.ConfigureWindow); ok && v.ID == 2 {
				hidden = append(hidden, v)
			}
			continue
		default:
		}
		break
	}
	if len(hidden) != 1 || hidden[0].Visible || hidden[0].Activated {
		t.Fatalf("configures of window 2 after the hide: %+v", hidden)
	}
}

// With animations off both are instant.
func TestStashToggleOffIsInstant(t *testing.T) {
	c, ic, sc, _ := stashRig(t, false)
	act(c, ic, ActionToggleStashVisible)
	s := indicatorScene(t, c)
	if w := sceneWindow(t, s, 2); !w.Hidden || len(sc.rects) != 0 || c.animating() {
		t.Fatalf("hide with animations off: %+v, %d rects", w, len(sc.rects))
	}
	act(c, ic, ActionToggleStashVisible)
	s = indicatorScene(t, c)
	if w := sceneWindow(t, s, 2); w.Hidden || w.Fade != 0 || w.Zoom != 0 || len(sc.rects) != 0 || c.animating() {
		t.Fatalf("show with animations off: %+v, %d rects", w, len(sc.rects))
	}
}

// Navigating in the stash animates the peek veil: the window that leaves the
// centre darkens from 0, the one that reaches it clears from the veil.
func TestStashNavigationAnimatesPeekDim(t *testing.T) {
	c, ic, sc, _ := stashRig(t, true)
	if w := sceneWindow(t, indicatorScene(t, c), 3); w.Dim != 0.5 {
		t.Fatalf("setup: peek dim %v", w.Dim)
	}
	act(c, ic, ActionFocusColumnLeft) // 3 selected, 2 peeks on its right
	s := indicatorScene(t, c)
	if w := sceneWindow(t, s, 3); w.Dim != 0.5 || w.Focused != true {
		t.Fatalf("window 3 right after: %+v, want the veil it had", w)
	}
	if w := sceneWindow(t, s, 2); w.Dim != 0 {
		t.Fatalf("window 2 right after: dim %v, want 0 (it was selected)", w.Dim)
	}
	s = frame(t, c, ic, 40*time.Millisecond)
	if w := sceneWindow(t, s, 3); !(w.Dim > 0 && w.Dim < 0.5) {
		t.Fatalf("window 3 mid-way dim %v, want strictly between 0 and 0.5", w.Dim)
	}
	if w := sceneWindow(t, s, 2); !(w.Dim > 0 && w.Dim < 0.5) {
		t.Fatalf("window 2 mid-way dim %v, want strictly between 0 and 0.5", w.Dim)
	}
	s = frame(t, c, ic, 5*time.Second)
	if w := sceneWindow(t, s, 3); w.Dim != 0 {
		t.Fatalf("window 3 settled dim %v, want 0", w.Dim)
	}
	if w := sceneWindow(t, s, 2); w.Dim != 0.5 {
		t.Fatalf("window 2 settled dim %v, want 0.5", w.Dim)
	}
	if c.animating() || len(sc.rects) != 0 {
		t.Fatalf("still animating: %d rects", len(sc.rects))
	}
}

// A navigation that chains on a running one starts the veil where it was.
func TestStashNavigationChainsPeekDim(t *testing.T) {
	c, ic, _, _ := stashRig(t, true)
	act(c, ic, ActionFocusColumnLeft)
	indicatorScene(t, c)
	ic.now = ic.now.Add(30 * time.Millisecond)
	c.animate(ic.now, nil)
	mid := sceneWindow(t, indicatorScene(t, c), 3).Dim
	act(c, ic, ActionFocusColumnRight) // 3 goes back to a peek
	if got := sceneWindow(t, indicatorScene(t, c), 3).Dim; got < mid-0.02 || got > mid+0.02 {
		t.Fatalf("window 3 veil %v after the chained action, want the %v it had", got, mid)
	}
}

// The overview pile: the stash cards shrink from the stash windows' rects
// and settle on the pile layout (C1's rules).
func TestOverviewPileScalesFromStash(t *testing.T) {
	c, ic, sc, _ := stashRig(t, true)
	from := map[WindowID]ports.Rect{}
	s := indicatorScene(t, c)
	for _, id := range []WindowID{2, 3} {
		from[id] = sceneWindow(t, s, id).Rect
	}
	toggleOverview(c, sc, ic.now)
	s = indicatorScene(t, c)
	for _, id := range []WindowID{2, 3} {
		w := sceneWindow(t, s, id)
		p := settledOf(t, sc, id)
		if w.Hidden || w.Preview != overviewCardZoom || w.Rect != from[id] || w.Zoom != 1 {
			t.Fatalf("window %d first frame %+v, want its stash rect %+v at content scale 1 (card %+v)", id, w, from[id], p.Rect)
		}
		checkContent(t, w, from[id], "first frame")
	}
	s = frame(t, c, ic, 40*time.Millisecond)
	for _, id := range []WindowID{2, 3} {
		w := sceneWindow(t, s, id)
		p := settledOf(t, sc, id)
		if !(w.Rect.W < from[id].W && w.Rect.W > p.Rect.W) || !(w.Zoom > overviewCardZoom && w.Zoom < 1) {
			t.Fatalf("window %d mid-way %+v, want between %+v and %+v", id, w, from[id], p.Rect)
		}
		checkContent(t, w, from[id], "mid-way")
	}
	s = frame(t, c, ic, 5*time.Second)
	for _, id := range []WindowID{2, 3} {
		w := sceneWindow(t, s, id)
		p := settledOf(t, sc, id)
		if w.Rect != p.Rect || w.Zoom != 0 || w.Fade != 0 || w.Preview != overviewCardZoom {
			t.Fatalf("window %d settled %+v, want the pile card %+v", id, w, p)
		}
	}
}

// A navigation slides the stash view: the windows keep the places they
// were drawn at and move with one spring, with no rect motion of their own.
// What the view brings in is off screen until it reaches the margin; what
// it sends off goes once the slide ends.
func TestStashNavigationSlides(t *testing.T) {
	c, ic, sc, cmds := stashRig(t, true)
	w := sc.mon.Current()
	w.AddWindow(5)
	w.stashFocus = false                   // the new tile has the focus, not the stash
	c.applyAction(ActionToggleWindowStash) // stash 4 3 2 5: 5 selected, 2 peeks
	if len(w.Stash) != 4 || w.stashAt != 3 {
		t.Fatalf("setup: stash %+v at %d", w.Stash, w.stashAt)
	}
	settleShown(t, c)
	centre := sceneWindow(t, indicatorScene(t, c), 5).Rect
	act(c, ic, ActionFocusColumnLeft) // 2 selected, 3 comes in, 5 becomes a peek
	s := indicatorScene(t, c)
	if got := sceneWindow(t, s, 5); got.Hidden || got.Rect != centre || got.Dim != 0 {
		t.Fatalf("window 5 jumped: %+v, want %+v unveiled", got, centre)
	}
	if got := sceneWindow(t, s, 3); !got.Hidden {
		t.Fatalf("window 3 is on screen before the slide: %+v", got)
	}
	if len(sc.rects) != 0 || !w.stashMotion.on {
		t.Fatalf("%d rect motions, slide %v: the view slides alone", len(sc.rects), w.stashMotion.on)
	}
	if got := frame(t, c, ic, 40*time.Millisecond); !(sceneWindow(t, got, 5).Dim > 0) {
		t.Fatal("window 5 has no veil mid-slide")
	}
	settledAll := frame(t, c, ic, 5*time.Second)
	if got := sceneWindow(t, settledAll, 3); got.Hidden || got.Dim != 0.5 {
		t.Fatalf("window 3 settled %+v", got)
	}
	drainConfigures(cmds)
	act(c, ic, ActionFocusColumnLeft) // 3 selected, 4 comes in, 5 goes
	frame(t, c, ic, 5*time.Second)
	s = indicatorScene(t, c)
	if got := sceneWindow(t, s, 5); !got.Hidden {
		t.Fatalf("window 5 still drawn after the settle: %+v", got)
	}
	if got := sceneWindow(t, s, 4); got.Hidden {
		t.Fatalf("window 4 not at the margin: %+v", got)
	}
	if c.animating() || w.stashOff != 0 {
		t.Fatal("still animating")
	}
}

// near fails the test unless a and b differ by less than eps.
func near(t *testing.T, what string, a, b, eps float64) {
	t.Helper()
	if math.Abs(a-b) > eps {
		t.Fatalf("%s jumped: %v then %v", what, a, b)
	}
}

// A hide during the show's fade-in continues from what was drawn: no jump
// in fade or rect, then it settles gone.
func TestStashHideDuringShowDoesNotJump(t *testing.T) {
	c, ic, sc, _ := stashRig(t, true)
	act(c, ic, ActionToggleStashVisible)
	indicatorScene(t, c)
	ic.now = ic.now.Add(5 * time.Second)
	c.animate(ic.now, nil)
	indicatorScene(t, c)
	act(c, ic, ActionToggleStashVisible) // show
	indicatorScene(t, c)
	s := frame(t, c, ic, 30*time.Millisecond)
	last := sceneWindow(t, s, 2)
	if !(last.Fade > 0 && last.Fade < 1) {
		t.Fatalf("setup: fade %v", last.Fade)
	}
	act(c, ic, ActionToggleStashVisible) // hide, same instant
	w := sceneWindow(t, indicatorScene(t, c), 2)
	if w.Hidden {
		t.Fatalf("window 2 vanished: %+v", w)
	}
	near(t, "fade", last.Fade, w.Fade, 0.02)
	near(t, "width", float64(last.Rect.W), float64(w.Rect.W), 2)
	near(t, "x", float64(last.Rect.X), float64(w.Rect.X), 2)
	near(t, "zoom", contentScale(last), contentScale(w), 0.03)
	s = frame(t, c, ic, 5*time.Second)
	if w := sceneWindow(t, s, 2); !w.Hidden || c.animating() || len(sc.rects) != 0 {
		t.Fatalf("after the settle: %+v, %d rects", w, len(sc.rects))
	}
}

// A show during the hide's fade-out continues from what was drawn.
func TestStashShowDuringHideDoesNotJump(t *testing.T) {
	c, ic, sc, _ := stashRig(t, true)
	act(c, ic, ActionToggleStashVisible) // hide
	indicatorScene(t, c)
	s := frame(t, c, ic, 30*time.Millisecond)
	last := sceneWindow(t, s, 2)
	if !(last.Fade > 0 && last.Fade < 1) {
		t.Fatalf("setup: fade %v", last.Fade)
	}
	act(c, ic, ActionToggleStashVisible) // show
	w := sceneWindow(t, indicatorScene(t, c), 2)
	if w.Hidden {
		t.Fatalf("window 2 hidden: %+v", w)
	}
	near(t, "fade", last.Fade, w.Fade, 0.02)
	near(t, "width", float64(last.Rect.W), float64(w.Rect.W), 2)
	near(t, "x", float64(last.Rect.X), float64(w.Rect.X), 2)
	near(t, "zoom", contentScale(last), contentScale(w), 0.03)
	// The entrance now runs back to opaque (the fade's speed carries over:
	// it may still rise a few frames before it turns).
	s = frame(t, c, ic, 150*time.Millisecond)
	if n := sceneWindow(t, s, 2); !(n.Fade < w.Fade) {
		t.Fatalf("fade %v then %v, want it to clear", w.Fade, n.Fade)
	}
	s = frame(t, c, ic, 5*time.Second)
	p := settledOf(t, sc, 2)
	if w := sceneWindow(t, s, 2); w.Fade != 0 || w.Zoom != 0 || w.Rect != p.Rect || c.animating() {
		t.Fatalf("settled %+v, want %+v", w, p.Rect)
	}
}

// A second navigation during the slide goes on from the view as drawn: no
// window jumps, and the one sent off goes once the slide ends.
func TestStashNavigationChainsFromSlide(t *testing.T) {
	c, ic, _, _ := stashRig(t, true)
	// 2 selected, 3 peeks left. Left: 3 centred, 2 peeks right, 4 enters.
	act(c, ic, ActionFocusColumnLeft)
	indicatorScene(t, c)
	s := frame(t, c, ic, 30*time.Millisecond)
	last := sceneWindow(t, s, 2)
	if !c.cur().mon.Current().stashMotion.on {
		t.Fatal("setup: the slide already ended")
	}
	// Left again: 4 centred, 3 peeks right, 2 goes off.
	act(c, ic, ActionFocusColumnLeft)
	w := sceneWindow(t, indicatorScene(t, c), 2)
	if w.Hidden {
		t.Fatalf("window 2 vanished: %+v", w)
	}
	near(t, "x", float64(last.Rect.X), float64(w.Rect.X), 2)
	near(t, "width", float64(last.Rect.W), float64(w.Rect.W), 2)
	near(t, "dim", last.Dim, w.Dim, 0.03)
	if s = frame(t, c, ic, 5*time.Second); !sceneWindow(t, s, 2).Hidden {
		t.Fatal("window 2 still drawn after the settle")
	}
}

// Leaving entries land in the same order whatever the map's: a hidden stash
// float (replaced in place) and absent tiles and floats (ID order, tiles
// before the first float, floats last).
func TestWithLeavingOrderIsDeterministic(t *testing.T) {
	c, ic := indicatorCore(t)
	c.cfg.Animations.On = true
	sc := c.cur()
	w := sc.mon.Current()
	for id := WindowID(1); id <= 4; id++ {
		sc.mon.AddWindow(id)
	}
	w.FocusID(4)
	c.applyAction(ActionToggleWindowStash) // 4 stashed, shown
	w.AddFloating(5, 20, 10)
	settleShown(t, c)
	var left []Placement
	for _, p := range sc.settledLayout {
		if p.ID == 2 || p.ID == 3 || p.ID == 4 || p.ID == 5 {
			left = append(left, p)
		}
	}
	if len(left) != 4 {
		t.Fatalf("setup: %d placements", len(left))
	}
	// 2 and 3 close (absent tiles), 5 closes (absent float), 4 hides.
	sc.mon.RemoveWindow(2)
	sc.mon.RemoveWindow(3)
	sc.mon.RemoveWindow(5)
	c.applyAction(ActionToggleStashVisible)
	for _, p := range left {
		c.leaveFrom(sc, p, nil, ic.now)
	}
	var want []WindowID
	for i := range 40 {
		c.refreshShown()
		var ids []WindowID
		for _, p := range sc.settledLayout {
			ids = append(ids, p.ID)
		}
		if i == 0 {
			want = ids
			if n := len(ids); n < 4 {
				t.Fatalf("layout %v", ids)
			}
			i2, i3 := slices.Index(ids, 2), slices.Index(ids, 3)
			i4, i5 := slices.Index(ids, 4), slices.Index(ids, 5)
			// Absent tiles by ID, then the leaving stash float (in its
			// slot), the absent float last.
			if !(i2 >= 0 && i2 < i3 && i3 < i4 && i4 < i5 && i5 == len(ids)-1) {
				t.Fatalf("order %v: want tiles 2, 3, then the stash float 4, then 5", ids)
			}
			if k := slices.Index(ids, 4); !sc.settledLayout[k].Leaving {
				t.Fatal("the hidden stash window is not replaced in place")
			}
			continue
		}
		if !slices.Equal(ids, want) {
			t.Fatalf("run %d: order %v, first run %v", i, ids, want)
		}
	}
}
