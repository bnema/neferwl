package vulkan

import (
	"image"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/stretchr/testify/require"
)

// orientedSquare is the target mask of a scene mask on scene rect r,
// derived geometrically: each square corner's pixel goes through t and
// names the corner of the oriented rect it lands in.
func orientedSquare(t domain.BufferTransform, r image.Rectangle, mask uint32, sw, sh int) uint32 {
	o := t.RectToBuffer(r, sw, sh)
	var out uint32
	for bit := range uint32(4) {
		if mask&(1<<bit) == 0 {
			continue
		}
		px, py := float64(r.Min.X)+0.5, float64(r.Min.Y)+0.5
		if bit&1 != 0 {
			px = float64(r.Max.X) - 0.5
		}
		if bit&2 != 0 {
			py = float64(r.Max.Y) - 0.5
		}
		x, y := t.ToBuffer(px, py, float64(sw), float64(sh))
		var c uint32
		if x > float64(o.Min.X+o.Max.X)/2 {
			c |= 1
		}
		if y > float64(o.Min.Y+o.Max.Y)/2 {
			c |= 2
		}
		out |= 1 << c
	}
	return out
}

// On a rotated or flipped output, the square corners of a tab bar and of
// its visible tab stay the ones they share, in target space.
func TestOrientSquareCorners(t *testing.T) {
	for _, transform := range []domain.BufferTransform{1, 4} {
		tw, th := transform.Size(256, 128)
		r := newTestRenderer(t, tw, th)
		sw, sh := r.sceneSize(transform)
		s := ringedTab(true)
		s.Transform = transform
		ds := r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, sw, sh)))
		type want struct {
			round image.Rectangle
			mask  uint32
		}
		var wants []want
		for _, d := range ds {
			if d.pc.misc[1]&flagRounded != 0 && d.square != 0 {
				round := image.Rect(int(d.round[0]), int(d.round[1]), int(d.round[2]), int(d.round[3]))
				wants = append(wants, want{round, orientedSquare(transform, round, d.square, sw, sh)})
			}
		}
		var bar, tab bool
		for _, w := range wants {
			bar = bar || w.mask == orientedSquare(transform, w.round, cornersBottom, sw, sh)
			tab = tab || w.mask == orientedSquare(transform, w.round, cornersTop, sw, sh)
		}
		require.True(t, bar && tab, "transform %d: bar and tab draws", transform)
		r.orient(ds, transform)
		i := 0
		for _, d := range ds {
			if d.pc.misc[1]&flagRounded != 0 && d.square != 0 {
				require.Equal(t, wants[i].mask, d.square, "transform %d draw %v", transform, wants[i].round)
				i++
			}
		}
		require.Equal(t, len(wants), i)

		// One corner alone.
		one := []draw{{}}
		roundedDraw(&one[0], image.Rect(10, 20, 40, 60), 4, cornerTopLeft)
		one[0].pc.rect = [4]int32{10, 20, 40, 60}
		wantOne := orientedSquare(transform, image.Rect(10, 20, 40, 60), cornerTopLeft, sw, sh)
		r.orient(one, transform)
		require.Equal(t, wantOne, one[0].square, "transform %d top-left", transform)
	}
}
