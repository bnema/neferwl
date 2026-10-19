package vulkan

import (
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
	"image"
	"testing"
)

func TestPositionHintGeometryDamageAllocations(t *testing.T) {
	for _, scale := range []float64{1, 1.5, 2} {
		r := &Renderer{width: int(640 * scale), height: int(480 * scale)}
		s := ports.Scene{OutputWidth: 640, OutputHeight: 480, Scale: scale, Border: ports.Border{Active: "#abcdef"}, PositionHint: ports.PositionHint{Opacity: 1, Columns: 2, Counts: [16]uint8{2, 1}, ActiveRow: 1}}
		dmg := newDamage(&target{}, s, image.Rect(0, 0, r.width, r.height))
		draws := r.draws(s, nil, dmg)
		require.NotEmpty(t, draws)
		w := sceneWalk{scale: scale}
		b := s.PositionHint.Bounds(640, 480)
		want := w.physRect(b.X, b.Y, b.W, b.H)
		var area image.Rectangle
		for _, d := range draws {
			area = area.Union(image.Rect(int(d.pc.rect[0]), int(d.pc.rect[1]), int(d.pc.rect[2]), int(d.pc.rect[3])))
		}
		require.Equal(t, want, area)
		require.Zero(t, testing.AllocsPerRun(100, func() { r.draws(s, nil, dmg) }))
		old := s
		old.PositionHint = ports.PositionHint{}
		phys := func(b ports.Rect) image.Rectangle { return w.physRect(b.X, b.Y, b.W, b.H) }
		got, ok := sceneDelta(old, s, phys, r.width*r.height)
		require.True(t, ok)
		require.Equal(t, want, got)
		s.PositionHint.Opacity = 0.5
		got, ok = sceneDelta(old, s, phys, r.width*r.height)
		require.True(t, ok)
		require.Equal(t, want, got)
	}
}

func TestPositionHintDamageTransitions(t *testing.T) {
	base := ports.Scene{Scale: 1, OutputWidth: 640, OutputHeight: 480, PositionHint: ports.PositionHint{Opacity: 1, Columns: 2, Counts: [16]uint8{2, 1}}}
	phys := func(r ports.Rect) image.Rectangle { return image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H) }
	bounds := func(s ports.Scene) image.Rectangle { return phys(s.PositionHint.Bounds(s.OutputWidth, s.OutputHeight)) }
	t.Run("disappear", func(t *testing.T) {
		cur := base
		cur.PositionHint = ports.PositionHint{}
		got, ok := sceneDelta(base, cur, phys, 640*480)
		require.True(t, ok)
		require.Equal(t, bounds(base), got)
	})
	t.Run("shape", func(t *testing.T) {
		cur := base
		cur.PositionHint.Columns = 3
		cur.PositionHint.Counts[2] = 4
		got, ok := sceneDelta(base, cur, phys, 640*480)
		require.True(t, ok)
		require.Equal(t, bounds(base).Union(bounds(cur)), got)
	})
	t.Run("fade", func(t *testing.T) {
		cur := base
		cur.PositionHint.Opacity = 0.5
		got, ok := sceneDelta(base, cur, phys, 640*480)
		require.True(t, ok)
		require.Equal(t, bounds(base), got)
	})
	t.Run("scale", func(t *testing.T) {
		cur := base
		cur.Scale = 2
		_, ok := sceneDelta(base, cur, phys, 640*480)
		require.False(t, ok, "scale change requires full target redraw; geometry scaling is checked separately")
	})
}
