package core

import (
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
	"testing"
	"testing/synctest"
)

func squareOverviewOutline(dst []ports.Separator, layout []Placement, b int) []ports.Separator {
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
				divider := ports.Separator{Rect: Rect{W: 200, H: 1}}
				strips, outline := overviewOutline([]ports.Separator{divider}, layout, 2, tc.gap, tc.radius)
				if tc.rounded {
					require.Equal(t, []ports.Separator{divider}, strips)
					require.Equal(t, &ports.SelectionOutline{Rect: Rect{X: 18, Y: 18, W: 104, H: 84}, Width: 2, Radius: 14, Active: true}, outline)
				} else {
					require.Nil(t, outline)
					require.Equal(t, append([]ports.Separator{divider}, squareOverviewOutline(nil, layout, 2)...), strips)
				}

			})
		})
	}
}
