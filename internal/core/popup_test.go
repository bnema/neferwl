package core

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestPlacePopup(t *testing.T) {
	menu := ports.Positioner{
		Width: 40, Height: 30,
		AnchorRect: Rect{X: 10, Y: 10, W: 20, H: 10},
		Anchor:     ports.EdgeBottomRight, Gravity: ports.EdgeBottomRight,
	}
	bounds := Rect{X: 0, Y: 0, W: 100, H: 100}
	for _, tc := range []struct {
		name   string
		mod    func(p *ports.Positioner)
		bounds Rect
		want   Rect
	}{
		{"fits", func(*ports.Positioner) {}, bounds, Rect{X: 30, Y: 20, W: 40, H: 30}},
		{"no bounds", func(p *ports.Positioner) { p.OffsetX = 500 }, Rect{}, Rect{X: 530, Y: 20, W: 40, H: 30}},
		{"centre", func(p *ports.Positioner) { p.Anchor, p.Gravity = 0, 0 }, bounds, Rect{X: 0, Y: 0, W: 40, H: 30}},
		{"flip x", func(p *ports.Positioner) { p.OffsetX = 40; p.Adjust = ports.AdjustFlipX }, Rect{W: 90, H: 100}, Rect{X: 10, Y: 20, W: 40, H: 30}},
		{"flip y", func(p *ports.Positioner) { p.AnchorRect.Y = 70; p.Adjust = ports.AdjustFlipY }, bounds, Rect{X: 30, Y: 40, W: 40, H: 30}},
		{"slide", func(p *ports.Positioner) { p.AnchorRect.X = 80; p.Adjust = ports.AdjustSlideX }, bounds, Rect{X: 60, Y: 20, W: 40, H: 30}},
		{"resize", func(p *ports.Positioner) { p.AnchorRect.X = 80; p.Adjust = ports.AdjustResizeX }, bounds, Rect{X: 100, Y: 20, W: 40, H: 30}},
		{"resize y", func(p *ports.Positioner) { p.AnchorRect.Y = 70; p.Adjust = ports.AdjustResizeY }, bounds, Rect{X: 30, Y: 80, W: 40, H: 20}},
		{"none", func(p *ports.Positioner) { p.AnchorRect.X = 80 }, bounds, Rect{X: 100, Y: 20, W: 40, H: 30}},
	} {
		p := menu
		tc.mod(&p)
		if got := place(p, tc.bounds); got != tc.want {
			t.Errorf("%s: got %+v want %+v", tc.name, got, tc.want)
		}
	}
}

func TestFloatingWindows(t *testing.T) {
	w := workspace()
	w.AddWindow(1)
	w.AddFloating(2, 20, 10)
	if id, _ := w.Focused(); id != 2 {
		t.Fatalf("focus %d, want the floating window", id)
	}
	l := w.Layout()
	f := l[len(l)-1]
	u := w.Usable
	if f.ID != 2 || !f.Floating || !f.Focused || f.Rect.W != 20 || f.Rect.H != 10 ||
		f.Rect.X != u.X+(u.W-20)/2 || f.Rect.Y != u.Y+(u.H-10)/2 {
		t.Fatalf("float placement %+v in %+v", f, u)
	}
	w.ResizeFloating(2, 500, 500)
	if r := w.Layout()[1].Rect; r.W != u.W || r.H != u.H {
		t.Fatalf("clamped %+v", r)
	}
	// A dialog from a fullscreen window waits hidden: the fullscreen is
	// exclusive and keeps the focus. It shows, focused, once it leaves.
	w.SetFullscreen(1, true)
	w.RemoveWindow(2)
	w.AddFloating(2, 20, 10)
	if l := w.Layout(); !l[1].Hidden || l[1].Focused || !l[0].Focused {
		t.Fatalf("float over fullscreen %+v", l)
	}
	w.SetFullscreen(1, false)
	if l := w.Layout(); l[1].Hidden || !l[1].Focused {
		t.Fatalf("float after fullscreen %+v", l)
	}
	w.RemoveWindow(2)
	if id, _ := w.Focused(); id != 1 || len(w.Floats) != 0 {
		t.Fatalf("focus after close %d", id)
	}
}
