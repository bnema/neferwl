package core

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/stretchr/testify/require"
)

func squareOverviewOutline(dst []domain.Separator, layout []Placement, b int) []domain.Separator {
	dst, _ = overviewOutline(dst, layout, b, 0, 0)
	return dst
}

func TestOverviewRoundedOutline(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		gap, radius                  int
		floating, card, cut, rounded bool
	}{
		{name: "tile gaps", gap: 8, radius: 12, rounded: true},
		{name: "tile no gaps", radius: 12},
		{name: "float no gaps", radius: 12, floating: true, rounded: true},
		{name: "square", gap: 8},
		{name: "cut", gap: 8, radius: 12, cut: true},
		{name: "selected card", radius: 12, floating: true, card: true, cut: true, rounded: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := Placement{Rect: Rect{X: 20, Y: 20, W: 100, H: 80}, Preview: 0.5, Focused: true, Floating: tc.floating, Card: tc.card}
				layout := []Placement{p}
				if tc.cut {
					layout = append(layout, Placement{Rect: Rect{X: 80, Y: 20, W: 60, H: 40}, Preview: 0.5, Card: true})
				}
				divider := domain.Separator{Rect: Rect{W: 200, H: 1}}
				strips, outline := overviewOutline([]domain.Separator{divider}, layout, 2, tc.gap, tc.radius)
				if tc.rounded {
					require.Equal(t, []domain.Separator{divider}, strips)
					require.Equal(t, &domain.SelectionOutline{Rect: Rect{X: 18, Y: 18, W: 104, H: 84}, Width: 2, Radius: 14, Active: true}, outline)
				} else {
					require.Nil(t, outline)
					require.Equal(t, append([]domain.Separator{divider}, squareOverviewOutline(nil, layout, 2)...), strips)
				}

			})
		})
	}
}
