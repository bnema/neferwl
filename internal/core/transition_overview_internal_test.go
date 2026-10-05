package core

import (
	"context"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// overviewRig is indicatorCore with animations on, windows 1 and 2 in two
// columns of the first workspace and window 3 alone on the second, settled,
// the first workspace on screen.
func overviewRig(t *testing.T) (*Core, *indicatorClock, *screen) {
	t.Helper()
	c, ic := indicatorCore(t)
	c.cfg.Animations.On = true
	sc := c.cur()
	m := sc.mon
	m.AddWindow(1)
	m.AddWindow(2)
	m.Focus(1)
	m.AddWindow(3)
	m.Focus(0)
	settleShown(t, c)
	return c, ic, sc
}

// toggleOverview is what a bind does: snapshot, toggle, transition.
func toggleOverview(c *Core, sc *screen, now time.Time) {
	shots := c.snapshot(now)
	sc.mon.ToggleOverview()
	c.transition(shots, now)
}

// frame moves the clock by d, steps the springs and returns the scene.
func frame(t *testing.T, c *Core, ic *indicatorClock, d time.Duration) ports.Scene {
	t.Helper()
	ic.now = ic.now.Add(d)
	c.animate(ic.now, nil)
	return indicatorScene(t, c)
}

// contentScale is the scale the renderer draws the window's content at:
// Zoom, else Preview, else 1 (vulkan walk).
func contentScale(w ports.SceneWindow) float64 {
	if w.Zoom > 0 {
		return w.Zoom
	}
	if w.Preview > 0 {
		return w.Preview
	}
	return 1
}

// checkContent fails unless the content scale the renderer will use draws
// the content at the frame's size (within rounding) and never magnifies it.
func checkContent(t *testing.T, w ports.SceneWindow, full ports.Rect, when string) {
	t.Helper()
	k := contentScale(w)
	if k <= 0 || k > 1 {
		t.Fatalf("%s: content scale %v out of (0, 1]: %+v", when, k, w)
	}
	if got := k * float64(full.W); math.Abs(got-float64(w.Rect.W)) > 1.5 {
		t.Fatalf("%s: content drawn %.1f wide in a %d wide frame (scale %v, full %d): %+v", when, got, w.Rect.W, k, full.W, w)
	}
	if got := k * float64(full.H); math.Abs(got-float64(w.Rect.H)) > 1.5 {
		t.Fatalf("%s: content drawn %.1f high in a %d high frame (scale %v, full %d): %+v", when, got, w.Rect.H, k, full.H, w)
	}
}

// Opening: a card starts at its window's rect with its content at full
// size, shrinks to its overview card with the content following every frame,
// and settles on the exact overview layout; the other rows fade in. Closing
// mirrors it and animations off are instant.
func TestOverviewOpenCloseAnimatesCards(t *testing.T) {
	c, ic, sc := overviewRig(t)
	full := map[WindowID]ports.Rect{}
	for _, id := range []WindowID{1, 2} {
		full[id] = sceneWindow(t, indicatorScene(t, c), id).Rect
	}

	toggleOverview(c, sc, ic.now)
	if !sc.mon.ov.open || !c.animating() {
		t.Fatalf("overview open %v, animating %v", sc.mon.ov.open, c.animating())
	}
	s := indicatorScene(t, c)
	card := map[WindowID]Placement{}
	for _, p := range sc.settledLayout {
		card[p.ID] = p
	}
	for _, id := range []WindowID{1, 2} {
		w := sceneWindow(t, s, id)
		settled := sc.mon.Layout()[slices.IndexFunc(sc.mon.Layout(), func(p Placement) bool { return p.ID == id })]
		if w.Rect != full[id] || w.Preview != settled.Preview || w.Zoom != 1 {
			t.Fatalf("window %d first frame %+v, want the full rect %+v at content scale 1 (card preview %v)", id, w, full[id], settled.Preview)
		}
		checkContent(t, w, full[id], "first frame")
	}
	// The other row: invisible, drawn as a card, no motion of its own.
	w3 := sceneWindow(t, s, 3)
	if w3.Hidden || w3.Fade != 1 || w3.Preview == 0 || w3.Zoom != 0 {
		t.Fatalf("other row first frame %+v, want invisible card without zoom", w3)
	}

	for i := range 6 {
		s = frame(t, c, ic, 25*time.Millisecond)
		for _, id := range []WindowID{1, 2} {
			w := sceneWindow(t, s, id)
			checkContent(t, w, full[id], "opening")
			if i == 1 {
				p := sc.mon.Layout()[slices.IndexFunc(sc.mon.Layout(), func(p Placement) bool { return p.ID == id })]
				if !(w.Rect.W < full[id].W && w.Rect.W > p.Rect.W) || !(w.Zoom > p.Preview && w.Zoom < 1) {
					t.Fatalf("window %d mid-way %+v, want a frame between %+v and %+v and a zoom between %v and 1", id, w, full[id], p.Rect, p.Preview)
				}
			}
		}
		if i == 1 {
			if w := sceneWindow(t, s, 3); !(w.Fade > 0 && w.Fade < 1) {
				t.Fatalf("other row mid-way fade %v, want strictly between 0 and 1", w.Fade)
			}
		}
	}
	s = frame(t, c, ic, 5*time.Second)
	for _, p := range sc.mon.Layout() {
		if p.Hidden {
			continue
		}
		w := sceneWindow(t, s, p.ID)
		if w.Rect != p.Rect || w.Zoom != 0 || w.Fade != 0 || w.Preview != p.Preview {
			t.Fatalf("settled window %d %+v, want the exact overview layout %+v", p.ID, w, p)
		}
	}
	if c.animating() || len(sc.rects) != 0 {
		t.Fatalf("still animating after the settle: %d rects", len(sc.rects))
	}

	// Close: the cards grow back to the windows, content following.
	toggleOverview(c, sc, ic.now)
	if sc.mon.ov.open {
		t.Fatal("the overview did not close")
	}
	s = indicatorScene(t, c)
	for _, id := range []WindowID{1, 2} {
		w := sceneWindow(t, s, id)
		if w.Preview != 0 || w.Rect != card[id].Rect {
			t.Fatalf("window %d first closing frame %+v, want the card rect %+v as a window", id, w, card[id].Rect)
		}
		checkContent(t, w, full[id], "first closing frame")
	}
	for i := range 6 {
		s = frame(t, c, ic, 25*time.Millisecond)
		for _, id := range []WindowID{1, 2} {
			w := sceneWindow(t, s, id)
			checkContent(t, w, full[id], "closing")
			if i == 1 && !(w.Rect.W > card[id].Rect.W && w.Rect.W < full[id].W && w.Zoom > 0 && w.Zoom < 1) {
				t.Fatalf("window %d mid-way closing %+v", id, w)
			}
		}
	}
	s = frame(t, c, ic, 5*time.Second)
	for _, id := range []WindowID{1, 2} {
		if w := sceneWindow(t, s, id); w.Rect != full[id] || w.Zoom != 0 || w.Preview != 0 {
			t.Fatalf("window %d settled after closing %+v, want %+v", id, w, full[id])
		}
	}
	if w := sceneWindow(t, s, 3); !w.Hidden {
		t.Fatalf("window 3 %+v after closing, want hidden (no fade-out)", w)
	}
	if c.animating() {
		t.Fatal("still animating after closing")
	}
}

// With animations off the overview opens and closes at once.
func TestOverviewOffIsInstant(t *testing.T) {
	c, ic, sc := overviewRig(t)
	c.cfg.Animations.On = false
	toggleOverview(c, sc, ic.now)
	s := indicatorScene(t, c)
	for _, p := range sc.mon.Layout() {
		if p.Hidden {
			continue
		}
		if w := sceneWindow(t, s, p.ID); w.Rect != p.Rect || w.Zoom != 0 || w.Fade != 0 {
			t.Fatalf("window %d %+v with animations off, want %+v", p.ID, w, p)
		}
	}
	if c.animating() || len(sc.rects) != 0 {
		t.Fatal("a motion runs with animations off")
	}
	toggleOverview(c, sc, ic.now)
	if indicatorScene(t, c); c.animating() || len(sc.rects) != 0 {
		t.Fatal("a motion runs after closing with animations off")
	}
}

// Moving the selection inside the open overview slides the cards (its rows
// scroll): same rect motions, contents scaled; the row that comes in fades.
func TestOverviewNavigationAnimates(t *testing.T) {
	c, ic, sc := overviewRig(t)
	toggleOverview(c, sc, ic.now)
	s := frame(t, c, ic, 5*time.Second)
	before := map[WindowID]ports.SceneWindow{}
	for _, w := range s.Windows {
		before[w.ID] = w
	}
	if !before[3].Hidden && before[3].Fade != 0 {
		t.Fatalf("setup: %+v", before[3])
	}
	// Down: the second row becomes the current one; rows scroll up.
	shots := c.snapshot(ic.now)
	sc.mon.OverviewMove(0, 1)
	c.transition(shots, ic.now)
	s = indicatorScene(t, c)
	moved := 0
	for _, w := range s.Windows {
		if w.Hidden {
			continue
		}
		b, ok := before[w.ID]
		if !ok || b.Hidden {
			continue
		}
		if w.Rect != b.Rect {
			t.Fatalf("window %d jumped on the first frame: %+v then %+v", w.ID, b.Rect, w.Rect)
		}
		moved++
	}
	if moved == 0 {
		t.Fatalf("no card was shown before and after: %+v", s.Windows)
	}
	s = frame(t, c, ic, 40*time.Millisecond)
	slid := false
	for _, w := range s.Windows {
		if b, ok := before[w.ID]; ok && !b.Hidden && !w.Hidden && w.Rect != b.Rect {
			slid = true
			// A card moving between cards keeps its content scale.
			if k := contentScale(w); k <= 0 || k > 1 {
				t.Fatalf("window %d scale %v", w.ID, k)
			}
		}
	}
	if !slid {
		t.Fatal("no card moved mid-way")
	}
	s = frame(t, c, ic, 5*time.Second)
	for _, p := range sc.mon.Layout() {
		if p.Hidden {
			continue
		}
		if w := sceneWindow(t, s, p.ID); w.Rect != p.Rect || w.Zoom != 0 || w.Fade != 0 {
			t.Fatalf("window %d settled %+v, want %+v", p.ID, w, p)
		}
	}
}

// A click while the cards move picks the card where it is drawn (the shown
// layout), and a click on a still overview is unchanged.
func TestOverviewClickDuringOpenUsesShownCards(t *testing.T) {
	c, ic, sc := overviewRig(t)
	toggleOverview(c, sc, ic.now)
	s := frame(t, c, ic, 30*time.Millisecond)
	// The pointer sits on the right half of the output: the shown card of
	// window 2 covers it; its settled card does not reach that far left.
	w2 := sceneWindow(t, s, 2)
	x, y := float64(w2.Rect.X+w2.Rect.W-1), float64(w2.Rect.Y+w2.Rect.H/2)
	settled := sc.mon.Layout()[slices.IndexFunc(sc.mon.Layout(), func(p Placement) bool { return p.ID == 2 })]
	if x < float64(settled.Rect.X+settled.Rect.W) {
		t.Fatalf("setup: x %v is inside the settled card %+v", x, settled.Rect)
	}
	if id := overviewIn(sc.mon.Layout(), x, y); id == 2 {
		t.Fatalf("setup: the settled layout picks 2 at %v,%v", x, y)
	}
	c.cursorX, c.cursorY = x+float64(sc.x), y+float64(sc.y)
	picked, err := c.overviewClick(context.Background())
	if err != nil || !picked {
		t.Fatalf("picked %v err %v during the opening", picked, err)
	}
	if sc.mon.ov.open {
		t.Fatal("the overview stayed open")
	}
	if id, _ := sc.mon.Focused(); id != 2 {
		t.Fatalf("focused %d, want 2 (the card drawn there)", id)
	}
}

// A scale motion on a card never magnifies content: a shown frame wider than
// the card's settled one gives at most scale 1, and a card in flight at its
// full size still carries Zoom 1 (Preview alone would shrink it).
func TestCardZoomClampedAndEmitted(t *testing.T) {
	var rm rectMotion
	rm.scale, rm.dw, rm.dh = true, 400, 240
	p := Placement{ID: 1, Rect: Rect{W: 100, H: 60}, Preview: overviewCardZoom}
	rm.show(&p)
	if p.Zoom != 1 || p.Preview != overviewCardZoom {
		t.Fatalf("shown %+v, want zoom clamped to 1 and the card's preview", p)
	}
	c, ic, sc := overviewRig(t)
	toggleOverview(c, sc, ic.now)
	if w := sceneWindow(t, indicatorScene(t, c), 1); w.Zoom != 1 || w.Preview == 0 {
		t.Fatalf("card at full size emitted %+v, want Zoom 1 with its Preview", w)
	}
}

// Rect motions belong to the overview state: a state change with no
// transition (animations off meanwhile, a session lock) drops them, and
// leaving the overview never keeps a card's motion on a window.
func TestOverviewRectsDroppedOnStateChange(t *testing.T) {
	c, ic, sc := overviewRig(t)
	toggleOverview(c, sc, ic.now)
	indicatorScene(t, c)
	if len(sc.rects) == 0 {
		t.Fatal("no motion for the opening")
	}
	sc.mon.ToggleOverview()
	indicatorScene(t, c)
	if len(sc.rects) != 0 {
		t.Fatalf("%d card motions survived the close without a transition", len(sc.rects))
	}
}

// The overview has no camera: toggling it, moving in it and closing it never
// start a column or workspace slide, right after the action (settled or not).
func TestOverviewStartsNoCameraSlide(t *testing.T) {
	c, ic, sc := overviewRig(t)
	m := sc.mon
	for _, id := range []WindowID{4, 5} {
		m.AddWindow(id) // more columns than fit: the view scrolls
	}
	settleShown(t, c)
	c.animate(ic.now.Add(5*time.Second), nil)
	ic.now = ic.now.Add(5 * time.Second)
	noCamera := func(when string) {
		t.Helper()
		if m.switchMotion.on || m.Current().motion.on {
			t.Fatalf("%s: a camera spring runs (switch %v, view %v)", when, m.switchMotion.on, m.Current().motion.on)
		}
	}
	noCamera("before")
	toggleOverview(c, sc, ic.now)
	noCamera("after open")
	frame(t, c, ic, 30*time.Millisecond)
	noCamera("mid open")
	for _, mv := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
		shots := c.snapshot(ic.now)
		m.OverviewMove(mv[0], mv[1])
		c.transition(shots, ic.now)
		noCamera("after move")
		frame(t, c, ic, 30*time.Millisecond)
	}
	toggleOverview(c, sc, ic.now)
	noCamera("after close")
	frame(t, c, ic, 5*time.Second)
	noCamera("settled")
}

// A card that still fades in (Fade >= 0.5) takes no click; a visible one
// does, and a faded card lets the one under it be picked.
func TestOverviewInSkipsFadedCards(t *testing.T) {
	r := Rect{X: 0, Y: 0, W: 100, H: 100}
	layout := []Placement{
		{ID: 1, Rect: r, Preview: 0.2},
		{ID: 2, Rect: r, Preview: 0.2, Fade: 0.6},
	}
	if id := overviewIn(layout, 50, 50); id != 1 {
		t.Fatalf("picked %d, want 1 (2 is nearly invisible)", id)
	}
	layout[1].Fade = 0.4
	if id := overviewIn(layout, 50, 50); id != 2 {
		t.Fatalf("picked %d, want 2 (visible enough)", id)
	}
	layout = layout[1:]
	layout[0].Fade = 0.9
	if id := overviewIn(layout, 50, 50); id != 0 {
		t.Fatalf("picked %d, want none", id)
	}
	// And through the shown layout of a running opening.
	c, ic, sc := overviewRig(t)
	toggleOverview(c, sc, ic.now)
	s := indicatorScene(t, c)
	w3 := sceneWindow(t, s, 3)
	if w3.Fade != 1 {
		t.Fatalf("setup: window 3 fade %v", w3.Fade)
	}
	if id := overviewIn(sc.shownLayout(), float64(w3.Rect.X+w3.Rect.W/2), float64(w3.Rect.Y+w3.Rect.H/2)); id == 3 {
		t.Fatal("the invisible card of the next row was picked")
	}
}

// A fade that runs on a card carries over a state change: picking it
// mid-fade (here by closing the overview on it) starts from the fade it
// had, it does not jump to opaque.
func TestOverviewCarriesFadeAcrossState(t *testing.T) {
	c, ic, sc := overviewRig(t)
	toggleOverview(c, sc, ic.now)
	s := frame(t, c, ic, 40*time.Millisecond)
	mid := sceneWindow(t, s, 3).Fade
	if !(mid > 0.05 && mid < 0.95) {
		t.Fatalf("setup: window 3 fade %v mid-way", mid)
	}
	shots := c.snapshot(ic.now)
	sc.mon.OverviewPick(3)
	c.transition(shots, ic.now)
	if sc.mon.ov.open {
		t.Fatal("the overview stayed open")
	}
	got := sceneWindow(t, indicatorScene(t, c), 3)
	if math.Abs(got.Fade-mid) > 0.02 {
		t.Fatalf("fade %v after the pick, want the sampled %v", got.Fade, mid)
	}
	s = frame(t, c, ic, 5*time.Second)
	if w := sceneWindow(t, s, 3); w.Fade != 0 {
		t.Fatalf("settled fade %v", w.Fade)
	}
}

// The content zoom follows the smaller of the width and height ratios: when
// the aspect does not hold, content never exceeds the frame.
func TestScaleZoomFollowsSmallerRatio(t *testing.T) {
	for _, tc := range []struct {
		name   string
		dw, dh float64
		want   float64
	}{
		{"narrower", -50, 0, 0.5},
		{"shorter", 0, -50, 0.5},
		{"both", -20, -40, 0.6},
		{"aspect kept", -30, -30, 0.7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rm := rectMotion{scale: true, dw: tc.dw, dh: tc.dh}
			p := Placement{ID: 1, Rect: Rect{W: 100, H: 100}}
			rm.show(&p)
			if math.Abs(p.Zoom-tc.want) > 1e-9 {
				t.Fatalf("zoom %v, want %v", p.Zoom, tc.want)
			}
			if float64(p.Rect.W)*1 < p.Zoom*100-1e-9 || float64(p.Rect.H) < p.Zoom*100-1e-9 {
				t.Fatalf("content %vx%v exceeds the frame %+v", p.Zoom*100, p.Zoom*100, p.Rect)
			}
		})
	}
}
