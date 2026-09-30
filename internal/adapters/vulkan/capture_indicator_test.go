package vulkan

import (
	"image"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

func indicatorScene(m ...ports.CaptureIndicator) ports.Scene {
	return ports.Scene{Seq: 1, Scale: 1, Background: "#101010", CaptureIndicators: m}
}

// indicatorDraws walks s and returns the physical rects drawn, all in the
// indicator red.
func indicatorDraws(t *testing.T, r *Renderer, s ports.Scene) []image.Rectangle {
	t.Helper()
	var out []image.Rectangle
	for _, d := range r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, r.width, r.height))) {
		require.Equal(t, [4]float32{0xff / 255.0, 0x3b / 255.0, 0x30 / 255.0, 1}, d.pc.color)
		out = append(out, image.Rect(int(d.pc.rect[0]), int(d.pc.rect[1]), int(d.pc.rect[2]), int(d.pc.rect[3])))
	}
	return out
}

func TestCaptureIndicatorBorderGeometryLogicalScaled(t *testing.T) {
	target := ports.CaptureIndicator{Rect: ports.Rect{X: 8, Y: 4, W: 16, H: 8}}
	for _, tc := range []struct {
		scale float64
		want  []image.Rectangle
	}{
		{1, []image.Rectangle{image.Rect(8, 4, 24, 6), image.Rect(8, 10, 24, 12), image.Rect(8, 6, 10, 10), image.Rect(22, 6, 24, 10)}},
		{2, []image.Rectangle{image.Rect(16, 8, 48, 12), image.Rect(16, 20, 48, 24), image.Rect(16, 12, 20, 20), image.Rect(44, 12, 48, 20)}},
	} {
		r := &Renderer{width: 64, height: 64}
		s := indicatorScene(target)
		s.Scale = tc.scale
		require.Equal(t, tc.want, indicatorDraws(t, r, s), "scale %v", tc.scale)
	}
	// On the output edge the border is still inside the output.
	r := &Renderer{width: 64, height: 64}
	got := indicatorDraws(t, r, indicatorScene(ports.CaptureIndicator{Rect: ports.Rect{W: 64, H: 64}}))
	require.Equal(t, []image.Rectangle{image.Rect(0, 0, 64, 2), image.Rect(0, 62, 64, 64), image.Rect(0, 2, 2, 62), image.Rect(62, 2, 64, 62)}, got)
	// A target thinner than the border is clamped: no overdraw.
	got = indicatorDraws(t, r, indicatorScene(ports.CaptureIndicator{Rect: ports.Rect{X: 4, Y: 4, W: 10, H: 2}}))
	require.Equal(t, []image.Rectangle{image.Rect(4, 4, 14, 5), image.Rect(4, 5, 14, 6)}, got)
	// Union of several targets, in order.
	got = indicatorDraws(t, r, indicatorScene(ports.CaptureIndicator{Rect: ports.Rect{W: 10, H: 10}}, ports.CaptureIndicator{Rect: ports.Rect{X: 20, W: 10, H: 10}}))
	require.Len(t, got, 8)
}

// A listed mark always draws something: a target 1 logical pixel wide or
// tall (an output that small, core inflates the others) is filled whole.
func TestCaptureIndicatorThinMarkStillDraws(t *testing.T) {
	r := &Renderer{width: 64, height: 64}
	got := indicatorDraws(t, r, indicatorScene(ports.CaptureIndicator{Rect: ports.Rect{X: 10, Y: 4, W: 1, H: 20}}))
	require.Equal(t, []image.Rectangle{image.Rect(10, 4, 11, 24)}, got, "W=1")
	got = indicatorDraws(t, r, indicatorScene(ports.CaptureIndicator{Rect: ports.Rect{X: 4, Y: 10, W: 20, H: 1}}))
	require.Equal(t, []image.Rectangle{image.Rect(4, 10, 24, 11)}, got, "H=1")
	s := indicatorScene(ports.CaptureIndicator{Rect: ports.Rect{X: 5, Y: 5, W: 1, H: 1}})
	s.Scale = 2
	require.Equal(t, []image.Rectangle{image.Rect(10, 10, 12, 12)}, indicatorDraws(t, &Renderer{width: 128, height: 128}, s))
}

func TestCaptureIndicatorPill(t *testing.T) {
	r := &Renderer{width: 64, height: 64}
	pill := ports.CaptureIndicator{Pill: true, Rect: ports.Rect{X: 44, Y: 8, W: 12, H: 12}}
	got := indicatorDraws(t, r, indicatorScene(pill))
	// Three bands: the corners are cut, the middle is the full width.
	require.Equal(t, []image.Rectangle{image.Rect(46, 8, 54, 11), image.Rect(44, 11, 56, 17), image.Rect(46, 17, 54, 20)}, got)
	s := indicatorScene(pill)
	s.Scale = 2
	got = indicatorDraws(t, &Renderer{width: 128, height: 128}, s)
	require.Equal(t, []image.Rectangle{image.Rect(91, 16, 109, 22), image.Rect(88, 22, 112, 34), image.Rect(91, 34, 109, 40)}, got)
}

// A scene without indicators draws nothing extra, and walking a scene that
// has them allocates nothing once warm.
func TestCaptureIndicatorIdleAndAllocs(t *testing.T) {
	r := &Renderer{width: 64, height: 64}
	s := indicatorScene()
	require.Empty(t, r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 64, 64))))
	walk := func(s ports.Scene) float64 {
		dmg := newDamage(&target{}, s, image.Rect(0, 0, 64, 64))
		r.draws(s, nil, dmg)
		return testing.AllocsPerRun(50, func() { r.draws(s, nil, dmg) })
	}
	idle := walk(s)
	with := walk(indicatorScene(ports.CaptureIndicator{Rect: ports.Rect{W: 64, H: 64}}, ports.CaptureIndicator{Pill: true, Rect: ports.Rect{X: 44, Y: 8, W: 12, H: 12}}))
	t.Logf("r.draws allocations: idle %.0f, with indicators %.0f", idle, with)
	require.LessOrEqual(t, with, idle, "indicator draws allocate")
}
