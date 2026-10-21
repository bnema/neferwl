package vulkan

import (
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
	"image"
	"math"
	"testing"
)

func TestFocusFlashGeometryAlphaAndOrder(t *testing.T) {
	for _, scale := range []int{1, 2} {
		r := &Renderer{width: 128, height: 128}
		s := ports.Scene{Scale: float64(scale), Border: ports.Border{Active: "#00ff00"},
			Separators:        []ports.Separator{{Rect: ports.Rect{W: 2, H: 20}, Active: true}},
			Flash:             &ports.FocusFlash{Rect: ports.Rect{X: 8, Y: 4, W: 16, H: 8}, Width: 2, Color: "#ff0000", Alpha: 0.5},
			CaptureIndicators: []ports.CaptureIndicator{{Rect: ports.Rect{W: 64, H: 64}}}}
		ds := r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 128, 128)))
		require.Len(t, ds, 9)
		require.Equal(t, [4]float32{0, 1, 0, 1}, ds[0].pc.color)
		for i, rect := range [][4]int32{{8, 4, 24, 6}, {8, 10, 24, 12}, {8, 6, 10, 10}, {22, 6, 24, 10}} {
			for j := range rect {
				rect[j] *= int32(scale)
			}
			require.Equal(t, rect, ds[i+1].pc.rect)
			require.Equal(t, float32(0.5), ds[i+1].pc.color[3])
			require.Greater(t, ds[i+1].pc.color[0], float32(0))
			require.Zero(t, ds[i+1].pc.color[1])
		}
		require.Equal(t, float32(1), ds[5].pc.color[3], "capture indicator remains opaque and above flash")
	}
}

func TestFocusFlashAbsentEmptyAndThin(t *testing.T) {
	r := &Renderer{width: 64, height: 64}
	for _, f := range []*ports.FocusFlash{nil, {Alpha: 0}, {Alpha: -1}, {Alpha: 1, Rect: ports.Rect{W: 0, H: 10}}} {
		s := ports.Scene{Scale: 1, Flash: f}
		require.Empty(t, r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 64, 64))))
	}
	for _, width := range []int{0, 8} {
		s := ports.Scene{Scale: 1, Flash: &ports.FocusFlash{Alpha: 1, Width: width, Rect: ports.Rect{X: 2, Y: 3, W: 1, H: 10}}}
		ds := r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 64, 64)))
		require.Len(t, ds, 1)
		require.Equal(t, [4]int32{2, 3, 3, 13}, ds[0].pc.rect)
	}
}

func TestFocusFlashRoundedRing(t *testing.T) {
	for _, scale := range []int{1, 2} {
		r := &Renderer{width: 128, height: 128}
		s := ports.Scene{Scale: float64(scale),
			Flash: &ports.FocusFlash{Rect: ports.Rect{X: 8, Y: 4, W: 40, H: 30}, Width: 3, Radius: 11, Color: "#ff0000", Alpha: 0.5}}
		ds := r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 128, 128)))
		require.Len(t, ds, 1)
		d := ds[0]
		require.Equal(t, uint32(flagRounded|flagRing), d.pc.misc[1]&(flagRounded|flagRing))
		require.Equal(t, [4]int32{8 * int32(scale), 4 * int32(scale), 48 * int32(scale), 34 * int32(scale)}, d.pc.rect)
		require.Equal(t, float32(0.5), d.pc.color[3])
		require.Equal(t, math.Float32bits(float32(11*scale)), d.radii[0])
		require.Equal(t, math.Float32bits(float32(8*scale)), d.radii[1])
		require.Equal(t, roundRect(image.Rect(11*scale, 7*scale, 45*scale, 31*scale)), d.roundInner)
	}
}

func TestFocusFlashZeroRadiusStaysSquare(t *testing.T) {
	r := &Renderer{width: 64, height: 64}
	s := ports.Scene{Scale: 1, Flash: &ports.FocusFlash{Rect: ports.Rect{X: 8, Y: 4, W: 16, H: 8}, Width: 2, Color: "#ff0000", Alpha: 1}}
	ds := r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 64, 64)))
	require.Len(t, ds, 4)
	for _, d := range ds {
		require.Zero(t, d.pc.misc[1]&(flagRounded|flagRing))
	}
}
