package core_test

import (
	"math"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func dropPreviewOf(t *testing.T, set []domain.Scene, output string) domain.DropPreview {
	t.Helper()
	p := dragScene(set, output).DropPreview
	if p == nil {
		t.Fatalf("no drop preview on %s", output)
	}
	return *p
}

func proxyIn(set []domain.Scene, output string) (domain.SceneWindow, int) {
	var got domain.SceneWindow
	n := 0
	for _, w := range dragScene(set, output).Windows {
		if w.Proxy {
			got = w
			n++
		}
	}
	return got, n
}

// Each anchor carries its action's label, the hint font size, and the
// window radius on window anchors only.
func TestDropPreviewLabels(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		r := startDragMode(t, animated, func(c *domain.Config) {
			c.Border.Radius = 8
			c.Layout.MaxColumns = 3
			c.Drop.SetEdge(domain.DropSideRight, domain.DropStash)
		}, dragOut)
		set := dragThreeColumns(t, r)
		one, two, three := dragRect(t, set, "A", 1), dragRect(t, set, "A", 2), dragRect(t, set, "A", 3)
		r.moveTo(float64(one.X+one.W/2), float64(one.Y+one.H/2), 1)
		r.settle(t)
		r.press(t, 0x110, domain.ModSuper)
		for _, c := range []struct {
			name   string
			x, y   int
			want   domain.DropAction
			radius int
		}{
			{"stack", three.X + three.W/2, three.Y + three.H - 40, domain.DropStack, 8},
			{"swap", two.X + two.W/2, two.Y + two.H/2, domain.DropSwap, 8},
			{"column", (two.X + two.W + three.X) / 2, two.Y + two.H/2, domain.DropColumn, 0},
			{"stash", 997, 300, domain.DropStash, 0},
		} {
			r.moveTo(float64(c.x), float64(c.y), 2)
			pp := dragScene(r.settle(t), "A").DropPreview
			if pp == nil {
				t.Fatalf("%s: no preview at %d,%d", c.name, c.x, c.y)
			}
			p := *pp
			if wantEdge := map[bool]domain.DropSide{true: domain.DropSideRight}[c.name == "stash"]; p.Edge != wantEdge {
				t.Fatalf("%s: edge %v", c.name, p.Edge)
			}
			if p.Label != c.want.Label() || p.Label == "" || p.Radius != c.radius || p.LabelPx <= 0 || p.Fill != 0.18 || p.Thumb != 0 {
				t.Fatalf("%s: preview %+v", c.name, p)
			}
		}
		if _, n := proxyIn(r.last, "A"); n != 0 {
			t.Fatal("a proxy for a window the output draws")
		}
		r.in(ports.KeyEvent{Keysym: "Escape", Mods: domain.ModSuper, Pressed: true})
		if r.settle(t); dragScene(r.last, "A").DropPreview != nil {
			t.Fatal("preview left after Escape")
		}
	})
}

// wantThumb checks the thumbnail of window id on output: a quarter of
// nat (at least 96 wide) at the pointer plus 12, clamped, drawn once by a
// Proxy window.
func wantThumb(t *testing.T, set []domain.Scene, output string, id domain.WindowID, nat domain.Rect, px, py, ow, oh int) {
	t.Helper()
	p := dropPreviewOf(t, set, output)
	w := max(int(math.Round(float64(nat.W)/4)), 96)
	h := int(math.Round(float64(nat.H) * float64(w) / float64(nat.W)))
	want := domain.Rect{X: min(px+12, ow-w), Y: min(py+12, oh-h), W: w, H: h}
	if p.Thumb != id || p.ThumbRect != want {
		t.Fatalf("thumb %d %+v, want %d %+v", p.Thumb, p.ThumbRect, id, want)
	}
	proxy, n := proxyIn(set, output)
	if n != 1 || proxy.ID != id || proxy.Rect != want || proxy.Zoom != float64(w)/float64(nat.W) || proxy.Tile() {
		t.Fatalf("proxy %+v (%d)", proxy, n)
	}
}

// On another output the dragged window gets a thumbnail; the pointer moves
// it, and the source output has neither preview nor proxy.
func TestDropPreviewThumbnailAcrossOutputs(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		r := startDragMode(t, animated, nil, dragOut, domain.OutputInfo{Name: "B", Width: 800, Height: 600})
		r.client <- ports.WindowMapped{ID: 1}
		r.client <- ports.WindowMapped{ID: 2}
		r.settle(t)
		r.moveTo(1500, 300, 1)
		r.settle(t)
		r.client <- ports.WindowMapped{ID: 3}
		set := r.settle(t)
		one := dragRect(t, set, "A", 1)
		r.moveTo(float64(one.X+one.W/2), float64(one.Y+one.H/2), 2)
		set = r.settle(t)
		if p := dragScene(set, "A").DropPreview; p != nil {
			t.Fatalf("preview before the press %+v", p)
		}
		r.press(t, 0x110, domain.ModSuper)
		r.moveTo(1400, 300, 3)
		set = r.settle(t)
		wantThumb(t, set, "B", 1, one, 400, 300, 800, 600)
		if _, n := proxyIn(set, "A"); n != 0 || dragScene(set, "A").DropPreview != nil {
			t.Fatal("source output shows the drag")
		}
		before := dropPreviewOf(t, set, "B")
		// Same anchor, pointer moved: the thumbnail follows.
		r.moveTo(1600, 430, 4)
		set = r.settle(t)
		wantThumb(t, set, "B", 1, one, 600, 430, 800, 600)
		if p := dropPreviewOf(t, set, "B"); p.Rect != before.Rect || p.Label != before.Label {
			t.Fatalf("zone changed with the pointer: %+v, was %+v", p, before)
		}
		// Clamped to the output.
		r.moveTo(1790, 590, 5)
		wantThumb(t, r.settle(t), "B", 1, one, 790, 590, 800, 600)
		r.in(ports.KeyEvent{Keysym: "Escape", Mods: domain.ModSuper, Pressed: true})
		if _, n := proxyIn(r.settle(t), "B"); n != 0 {
			t.Fatal("proxy left after Escape")
		}
	})
}

// After a hover switch hides the dragged window's workspace, the zone on
// the new one shows a thumbnail.
func TestDropPreviewThumbnailAfterWorkspaceSwitch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startDragRig(t, func(c *domain.Config) {
			c.Binds = map[string]string{"Super+s": "focus-workspace-next", "Super+a": "focus-workspace-prev"}
		}, dragOut)
		r.client <- ports.WindowMapped{ID: 1}
		r.client <- ports.WindowMapped{ID: 2}
		r.settle(t)
		r.in(ports.KeyEvent{Keysym: "s", Mods: domain.ModSuper, Pressed: true})
		r.in(ports.KeyEvent{Keysym: "s", Mods: domain.ModSuper})
		r.settle(t)
		r.client <- ports.WindowMapped{ID: 3}
		r.settle(t)
		r.in(ports.KeyEvent{Keysym: "a", Mods: domain.ModSuper, Pressed: true})
		r.in(ports.KeyEvent{Keysym: "a", Mods: domain.ModSuper})
		set := r.settle(t)
		one := dragRect(t, set, "A", 1)
		r.moveTo(float64(one.X+one.W/2), float64(one.Y+one.H/2), 1)
		r.settle(t)
		r.press(t, 0x110, domain.ModSuper)
		r.moveTo(500, 597, 2)
		set = r.settle(t)
		if p := dropPreviewOf(t, set, "A"); p.Thumb != 0 || p.Label != domain.DropWorkspace.Label() {
			t.Fatalf("before the switch %+v", p)
		}
		time.Sleep(dragHoverFirst)
		set = r.settle(t)
		three := dragRect(t, set, "A", 3)
		r.moveTo(float64(three.X+60), float64(three.Y+three.H/2), 3)
		set = r.settle(t)
		wantThumb(t, set, "A", 1, one, three.X+60, three.Y+three.H/2, 1000, 600)
		r.in(ports.KeyEvent{Keysym: "Escape", Mods: domain.ModSuper, Pressed: true})
		r.settle(t)
	})
}

const dragHoverFirst = 400 * time.Millisecond

// slideRig drags 1 onto the bottom band of 3, three columns, animations
// as given, real bubble time.
func slideRig(t *testing.T, on bool) (*dragRig, domain.Rect) {
	t.Helper()
	r := startDragRig(t, func(c *domain.Config) {
		c.Animations.On, c.Animations.Slowdown = on, 1
		c.Layout.MaxColumns = 3
	}, dragOut)
	dragThreeColumns(t, r)
	//testpolicy:sleep fake-time helper: callers run it in a synctest bubble
	time.Sleep(2 * time.Second)
	set := r.settle(t)
	one, three := dragRect(t, set, "A", 1), dragRect(t, set, "A", 3)
	r.moveTo(float64(one.X+one.W/2), float64(one.Y+one.H/2), 1)
	r.settle(t)
	r.press(t, 0x110, domain.ModSuper)
	r.moveTo(float64(three.X+three.W/2), float64(three.Y+three.H-40), 2)
	return r, three
}

// With animations on the zone slides to a new anchor over 100 ms: halfway
// at 50 ms, Animating until it lands.
func TestDropPreviewSlides(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, three := slideRig(t, true)
		from := dropPreviewOf(t, r.settle(t), "A").Rect
		time.Sleep(time.Second)
		if s := dragScene(r.settle(t), "A"); s.Animating || s.DropPreview.Rect != from {
			t.Fatalf("first zone animates: %+v", s.DropPreview)
		}
		r.moveTo(float64(three.X+three.W/2), float64(three.Y+40), 3)
		s := dragScene(r.settle(t), "A")
		if s.DropPreview.Rect != from || !s.Animating {
			t.Fatalf("start %+v animating %v", s.DropPreview.Rect, s.Animating)
		}
		time.Sleep(50 * time.Millisecond)
		half := dragScene(r.settle(t), "A")
		time.Sleep(50 * time.Millisecond)
		end := dragScene(r.settle(t), "A")
		to := end.DropPreview.Rect
		if to == from || end.Animating {
			t.Fatalf("end %+v animating %v", to, end.Animating)
		}
		want := domain.Rect{X: from.X, Y: (from.Y + to.Y) / 2, W: from.W, H: (from.H + to.H) / 2}
		if !half.Animating || half.DropPreview.Rect != want {
			t.Fatalf("halfway %+v, want %+v (animating %v)", half.DropPreview.Rect, want, half.Animating)
		}
		r.in(ports.KeyEvent{Keysym: "Escape", Mods: domain.ModSuper, Pressed: true})
		r.settle(t)
	})
}

// Off, the zone jumps to the new anchor.
func TestDropPreviewJumpsWithoutAnimations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, three := slideRig(t, false)
		from := dropPreviewOf(t, r.settle(t), "A").Rect
		r.moveTo(float64(three.X+three.W/2), float64(three.Y+40), 3)
		s := dragScene(r.settle(t), "A")
		if s.DropPreview.Rect == from || s.DropPreview.Rect.Y >= from.Y || s.Animating {
			t.Fatalf("zone %+v from %+v animating %v", s.DropPreview.Rect, from, s.Animating)
		}
		r.in(ports.KeyEvent{Keysym: "Escape", Mods: domain.ModSuper, Pressed: true})
		r.settle(t)
	})
}

// A hover edge's fill ramps from 0.06 to 0.18 while its timer runs, with
// animations on; off, it is 0.18 at once.
func TestDropPreviewHoverFillRamp(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[on], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := startDragRig(t, func(c *domain.Config) { c.Animations.On = on }, dragOut)
				r.client <- ports.WindowMapped{ID: 1}
				r.client <- ports.WindowMapped{ID: 2}
				time.Sleep(2 * time.Second)
				set := r.settle(t)
				one := dragRect(t, set, "A", 1)
				r.moveTo(float64(one.X+one.W/2), float64(one.Y+one.H/2), 1)
				r.settle(t)
				r.press(t, 0x110, domain.ModSuper)
				r.moveTo(500, 597, 2)
				start := dragScene(r.settle(t), "A")
				time.Sleep(200 * time.Millisecond)
				mid := dragScene(r.settle(t), "A")
				if start.DropPreview.Label != domain.DropWorkspace.Label() {
					t.Fatalf("not a hover edge: %+v", start.DropPreview)
				}
				if !on {
					if start.DropPreview.Fill != 0.18 || mid.DropPreview.Fill != 0.18 || mid.Animating {
						t.Fatalf("fill %v %v", start.DropPreview.Fill, mid.DropPreview.Fill)
					}
				} else if start.DropPreview.Fill != 0.06 || math.Abs(mid.DropPreview.Fill-0.12) > 1e-9 || !mid.Animating {
					t.Fatalf("fill %v %v animating %v", start.DropPreview.Fill, mid.DropPreview.Fill, mid.Animating)
				}
				r.in(ports.KeyEvent{Keysym: "Escape", Mods: domain.ModSuper, Pressed: true})
				r.settle(t)
			})
		})
	}
}
