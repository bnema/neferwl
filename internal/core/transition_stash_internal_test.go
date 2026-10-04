package core

import (
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

func settledPlacement(t *testing.T, sc *screen, id WindowID) Placement {
	t.Helper()
	i := slices.IndexFunc(sc.settledLayout, func(p Placement) bool { return p.ID == id })
	if i < 0 {
		t.Fatalf("window %d not in the settled layout", id)
	}
	return sc.settledLayout[i]
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
		p := settledPlacement(t, sc, id)
		w := sceneWindow(t, s, id)
		if w.Hidden || w.Fade != 1 || w.Rect != scaledRect(p.Rect, appearScale) || w.Zoom < 0.85 || w.Zoom > 0.95 {
			t.Fatalf("window %d first frame %+v, want invisible at 90 %% of %+v", id, w, p.Rect)
		}
	}
	if w := sceneWindow(t, s, 4); !w.Hidden || w.Fade != 0 {
		t.Fatalf("window 4 stays off screen: %+v", w)
	}
	// The action configures what it changes once, at the settled size.
	if ids := drainConfigures(cmds); !slices.Contains(ids, 2) {
		t.Fatalf("configures %v, want window 2 shown", ids)
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
		p := settledPlacement(t, sc, id)
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
		left[id] = settledPlacement(t, sc, id)
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
		p := settledPlacement(t, sc, id)
		if w.Hidden || w.Preview != overviewCardZoom || w.Rect != from[id] || w.Zoom != 1 {
			t.Fatalf("window %d first frame %+v, want its stash rect %+v at content scale 1 (card %+v)", id, w, from[id], p.Rect)
		}
		checkContent(t, w, from[id], "first frame")
	}
	s = frame(t, c, ic, 40*time.Millisecond)
	for _, id := range []WindowID{2, 3} {
		w := sceneWindow(t, s, id)
		p := settledPlacement(t, sc, id)
		if !(w.Rect.W < from[id].W && w.Rect.W > p.Rect.W) || !(w.Zoom > overviewCardZoom && w.Zoom < 1) {
			t.Fatalf("window %d mid-way %+v, want between %+v and %+v", id, w, from[id], p.Rect)
		}
		checkContent(t, w, from[id], "mid-way")
	}
	s = frame(t, c, ic, 5*time.Second)
	for _, id := range []WindowID{2, 3} {
		w := sceneWindow(t, s, id)
		p := settledPlacement(t, sc, id)
		if w.Rect != p.Rect || w.Zoom != 0 || w.Fade != 0 || w.Preview != overviewCardZoom {
			t.Fatalf("window %d settled %+v, want the pile card %+v", id, w, p)
		}
	}
}

// A navigation that brings a window to a margin makes it appear, one that
// sends a peek off screen makes it leave; the others only change their veil
// and rect.
func TestStashNavigationAppearsAndLeaves(t *testing.T) {
	c, ic, sc, cmds := stashRig(t, true)
	w := sc.mon.Current()
	w.AddWindow(5)
	w.stashFocus = false                   // the new tile has the focus, not the stash
	c.applyAction(ActionToggleWindowStash) // stash 4 3 2 5: 5 selected, 2 peeks
	if len(w.Stash) != 4 || w.stashAt != 3 {
		t.Fatalf("setup: stash %+v at %d", w.Stash, w.stashAt)
	}
	settleShown(t, c)
	act(c, ic, ActionFocusColumnLeft) // 2 selected, 3 comes in, 5 stays a peek
	s := indicatorScene(t, c)
	if w := sceneWindow(t, s, 3); w.Hidden || w.Fade != 1 {
		t.Fatalf("window 3 comes to the margin: %+v, want invisible at first", w)
	}
	if w := sceneWindow(t, s, 5); w.Hidden || w.Fade != 0 {
		t.Fatalf("window 5 stays a peek: %+v", w)
	}
	settledAll := frame(t, c, ic, 5*time.Second)
	if w := sceneWindow(t, settledAll, 3); w.Fade != 0 || w.Hidden {
		t.Fatalf("window 3 settled %+v", w)
	}
	drainConfigures(cmds)
	act(c, ic, ActionFocusColumnLeft) // 3 selected, 4 comes in, 5 goes
	s = indicatorScene(t, c)
	if w := sceneWindow(t, s, 4); w.Hidden || w.Fade != 1 {
		t.Fatalf("window 4 comes to the margin: %+v", w)
	}
	if w := sceneWindow(t, s, 5); w.Hidden || w.Fade != 0 || w.Rect.W == 0 {
		t.Fatalf("window 5 leaves from its peek rect: %+v", w)
	}
	s = frame(t, c, ic, 5*time.Second)
	if w := sceneWindow(t, s, 5); !w.Hidden {
		t.Fatalf("window 5 still drawn after the settle: %+v", w)
	}
	if c.animating() {
		t.Fatal("still animating")
	}
}
