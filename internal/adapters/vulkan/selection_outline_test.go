package vulkan

import (
	"image"
	"math"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestSelectionOutlineRoundedRing(t *testing.T) {
	for _, scale := range []int{1, 2} {
		r := &Renderer{width: 128, height: 128}
		s := ports.Scene{Scale: float64(scale), Border: ports.Border{Active: "#00ff00"}, Outline: &ports.SelectionOutline{Rect: domain.Rect{X: 8, Y: 4, W: 40, H: 30}, Width: 3, Radius: 11, Active: true}}
		ds := r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 128, 128)))
		require.Len(t, ds, 1)
		d := ds[0]
		require.Equal(t, uint32(flagRounded|flagRing), d.pc.misc[1]&(flagRounded|flagRing))
		require.Equal(t, [4]int32{8 * int32(scale), 4 * int32(scale), 48 * int32(scale), 34 * int32(scale)}, d.pc.rect)
		require.Equal(t, [4]float32{0, 1, 0, 1}, d.pc.color)
		require.Equal(t, math.Float32bits(float32(11*scale)), d.radii[0])
		require.Equal(t, math.Float32bits(float32(8*scale)), d.radii[1])
		require.Equal(t, roundRect(image.Rect(11*scale, 7*scale, 45*scale, 31*scale)), d.roundInner)
	}
}
