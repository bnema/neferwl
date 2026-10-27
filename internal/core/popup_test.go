package core

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestPlacePopup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		menu := domain.Positioner{
			Width: 40, Height: 30,
			AnchorRect: Rect{X: 10, Y: 10, W: 20, H: 10},
			Anchor:     ports.EdgeBottomRight, Gravity: ports.EdgeBottomRight,
		}
		bounds := Rect{X: 0, Y: 0, W: 100, H: 100}
		for _, tc := range []struct {
			name   string
			mod    func(p *domain.Positioner)
			bounds Rect
			want   Rect
		}{
			{"fits", func(*domain.Positioner) {}, bounds, Rect{X: 30, Y: 20, W: 40, H: 30}},
			{"no bounds", func(p *domain.Positioner) { p.OffsetX = 500 }, Rect{}, Rect{X: 530, Y: 20, W: 40, H: 30}},
			{"centre", func(p *domain.Positioner) { p.Anchor, p.Gravity = 0, 0 }, bounds, Rect{X: 0, Y: 0, W: 40, H: 30}},
			{"flip x", func(p *domain.Positioner) { p.OffsetX = 40; p.Adjust = ports.AdjustFlipX }, Rect{W: 90, H: 100}, Rect{X: 10, Y: 20, W: 40, H: 30}},
			{"flip y", func(p *domain.Positioner) { p.AnchorRect.Y = 70; p.Adjust = ports.AdjustFlipY }, bounds, Rect{X: 30, Y: 40, W: 40, H: 30}},
			{"slide", func(p *domain.Positioner) { p.AnchorRect.X = 80; p.Adjust = ports.AdjustSlideX }, bounds, Rect{X: 60, Y: 20, W: 40, H: 30}},
			{"resize", func(p *domain.Positioner) { p.AnchorRect.X = 80; p.Adjust = ports.AdjustResizeX }, bounds, Rect{X: 100, Y: 20, W: 40, H: 30}},
			{"resize y", func(p *domain.Positioner) { p.AnchorRect.Y = 70; p.Adjust = ports.AdjustResizeY }, bounds, Rect{X: 30, Y: 80, W: 40, H: 20}},
			{"none", func(p *domain.Positioner) { p.AnchorRect.X = 80 }, bounds, Rect{X: 100, Y: 20, W: 40, H: 30}},
		} {
			p := menu
			tc.mod(&p)
			if got := place(p, tc.bounds); got != tc.want {
				t.Errorf("%s: got %+v want %+v", tc.name, got, tc.want)
			}
		}

	})
}
