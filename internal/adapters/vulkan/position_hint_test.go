package vulkan

import (
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
	"image"
	"math"
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

func TestPositionHintRoundedQuads(t *testing.T) {
	for _, scale := range []float64{1, 2} {
		r := &Renderer{width: int(640 * scale), height: int(480 * scale)}
		h := hintGrid(3, 2)
		s := ports.Scene{OutputWidth: 640, OutputHeight: 480, Scale: scale, PositionHint: h}
		dmg := newDamage(&target{}, s, image.Rect(0, 0, r.width, r.height))
		draws := r.draws(s, nil, dmg)
		require.Len(t, draws, 1+6)
		w := sceneWalk{scale: scale}
		b := h.Bounds(640, 480)
		pill := w.physRect(b.X, b.Y, b.W, b.H)
		for i, d := range draws {
			require.NotZero(t, d.pc.misc[1]&flagRounded, "draw %d", i)
			require.Zero(t, d.pc.misc[1]&flagRing)
			require.Equal(t, roundRect(image.Rect(int(d.pc.rect[0]), int(d.pc.rect[1]), int(d.pc.rect[2]), int(d.pc.rect[3]))), d.round)
		}
		require.Equal(t, pill, image.Rect(int(draws[0].pc.rect[0]), int(draws[0].pc.rect[1]), int(draws[0].pc.rect[2]), int(draws[0].pc.rect[3])))
		require.Equal(t, float32(w.phys(ports.PositionHintRadius)), math.Float32frombits(draws[0].radii[0]))
		// Active dot (col 1,row 1) is the larger one: radius = phys(active diameter)/2.
		active := draws[1+1*2+1]
		require.Equal(t, float32(w.phys(ports.PositionHintActiveDiameter)/2), math.Float32frombits(active.radii[0]))
		other := draws[1]
		require.Equal(t, float32(w.phys(ports.PositionHintDiameter)/2), math.Float32frombits(other.radii[0]))
	}
}

func TestPositionHintRoundedOrient(t *testing.T) {
	r := &Renderer{width: 640, height: 480}
	s := ports.Scene{OutputWidth: 640, OutputHeight: 480, Scale: 1, PositionHint: hintGrid(2, 2)}
	dmg := newDamage(&target{}, s, image.Rect(0, 0, r.width, r.height))
	ds := r.draws(s, nil, dmg)
	r.orient(ds, ports.BufferTransform(1))
	for _, d := range ds {
		require.Equal(t, roundRect(image.Rect(int(d.pc.rect[0]), int(d.pc.rect[1]), int(d.pc.rect[2]), int(d.pc.rect[3]))), d.round)
	}
}

func TestHintRoundDraws(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 100)
	for _, tc := range []struct {
		name       string
		rect       image.Rectangle
		radius     int
		wantDraws  int
		wantRadius float32
	}{
		{"inside", image.Rect(10, 10, 50, 40), 6, 1, 6},
		{"clipped fully off output", image.Rect(200, 200, 240, 230), 6, 0, 0},
		{"clipped by bounds", image.Rect(90, 90, 130, 130), 6, 1, 6},
		{"tiny pill clamps the radius", image.Rect(10, 10, 16, 14), 100, 1, 2},
		{"zero radius is a plain fill", image.Rect(10, 10, 50, 40), 0, 1, 0},
		{"negative radius is a plain fill", image.Rect(10, 10, 50, 40), -3, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := sceneWalk{r: &Renderer{width: 100, height: 100}, alpha: 1, bounds: bounds}
			w.hintRound(tc.rect, tc.radius, [3]uint8{1, 2, 3})
			require.Len(t, w.draws, tc.wantDraws)
			for _, d := range w.draws {
				if tc.wantRadius == 0 {
					require.Zero(t, d.pc.misc[1]&flagRounded)
					continue
				}
				require.NotZero(t, d.pc.misc[1]&flagRounded)
				require.Equal(t, roundRect(tc.rect), d.round, "the clip is the requested rect")
				require.Equal(t, tc.wantRadius, math.Float32frombits(d.radii[0]))
			}
		})
	}
}

func TestPositionHintRoundedRotatedOrient(t *testing.T) {
	for transform := ports.BufferTransform(1); transform < 8; transform++ {
		r := &Renderer{width: 640, height: 480}
		sw, sh := r.sceneSize(transform)
		s := ports.Scene{OutputWidth: sw, OutputHeight: sh, Scale: 1, Transform: transform, PositionHint: hintGrid(2, 2)}
		ds := r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, sw, sh)))
		require.Len(t, ds, 1+4)
		sceneRects := make([]image.Rectangle, len(ds))
		for i, d := range ds {
			sceneRects[i] = image.Rect(int(d.pc.rect[0]), int(d.pc.rect[1]), int(d.pc.rect[2]), int(d.pc.rect[3]))
		}
		r.orient(ds, transform)
		for i, d := range ds {
			want := transform.RectToBuffer(sceneRects[i], sw, sh)
			require.Equal(t, roundRect(want), d.round, "transform %d draw %d", transform, i)
			require.NotZero(t, d.pc.misc[1]&flagRounded)
		}
	}
}
