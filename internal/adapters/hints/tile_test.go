package hints

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTileRegions(t *testing.T) {
	whole := [][2]int{{3840, 2160}, {1920, 1080}, {2560, 1080}, {2560, 1440}, {1280, 720}}
	for _, s := range whole {
		require.Nil(t, tileRegions(s[0], s[1]), "%dx%d", s[0], s[1])
	}
	require.Equal(t, []region{{0, 0, 2560, 1440}, {1280, 0, 2560, 1440}, {2560, 0, 2560, 1440},
		{0, 720, 2560, 1440}, {1280, 720, 2560, 1440}, {2560, 720, 2560, 1440}}, tileRegions(5120, 2160))
	require.Equal(t, []region{{0, 0, 2560, 1440}, {880, 0, 2560, 1440}}, tileRegions(3440, 1440))

	for _, s := range [][2]int{{5120, 2160}, {3440, 1440}, {5120, 1440}, {7680, 2160}, {3000, 2999}, {1000, 3000}, {5121, 2161}, {6000, 1001}} {
		w, h := s[0], s[1]
		rs := tileRegions(w, h)
		require.NotEmpty(t, rs, "%dx%d", w, h)
		require.LessOrEqual(t, len(rs), maxRegions)
		covered := make([]bool, w*h)
		for _, r := range rs {
			require.Equal(t, r.h*16, r.w*9, "%dx%d tile %+v is not 16:9", w, h, r)
			require.GreaterOrEqual(t, r.x, 0)
			require.GreaterOrEqual(t, r.y, 0)
			require.LessOrEqual(t, r.x+r.w, w)
			require.LessOrEqual(t, r.y+r.h, h)
			for y := r.y; y < r.y+r.h; y++ {
				for x := r.x; x < r.x+r.w; x++ {
					covered[y*w+x] = true
				}
			}
		}
		for i, c := range covered {
			if !c {
				t.Fatalf("%dx%d: pixel %d,%d not covered", w, h, i%w, i/w)
			}
		}
	}
}

func TestTileStartsOverlap(t *testing.T) {
	for _, l := range []int{2561, 3440, 5120, 7680, 10000} {
		xs := tileStarts(l, 2560)
		require.Equal(t, 0, xs[0])
		require.Equal(t, l-2560, xs[len(xs)-1])
		for i := 1; i < len(xs); i++ {
			require.GreaterOrEqual(t, xs[i-1]+2560-xs[i], 256, "length %d", l) // >= 10 %
		}
	}
}

func TestTileRegionsTooMany(t *testing.T) {
	require.Nil(t, tileRegions(60000, 20000))
}

func TestDropCutBoxes(t *testing.T) {
	rs := tileRegions(5120, 2160) // tile 0: x 0..2560, tile 1: x 1280..3840
	// A button at x 2500..2700 is cut at 2560 by tile 0 and whole in tile 1.
	cutBox := detection{x1: 2500, y1: 100, x2: 2560, y2: 140, score: 0.6, region: 0}
	whole := detection{x1: 2500, y1: 100, x2: 2700, y2: 140, score: 0.8, region: 1}
	require.Equal(t, []detection{whole}, dropCutBoxes([]detection{cutBox, whole}, rs))
	// Nobody saw it whole: the cut box stays.
	require.Equal(t, []detection{cutBox}, dropCutBoxes([]detection{cutBox}, rs))
	// An edge of the frame is not an inner edge.
	edge := detection{x1: 0, y1: 0, x2: 60, y2: 30, score: 0.6, region: 0}
	// Region 3 starts at y 720: its box at the frame's left edge is not cut
	// either, so both stay though they sit in different regions.
	other := detection{x1: 0, y1: 800, x2: 60, y2: 830, score: 0.5, region: 3}
	require.Equal(t, []detection{edge, other}, dropCutBoxes([]detection{edge, other}, rs))
	// No regions: unchanged.
	require.Equal(t, []detection{cutBox}, dropCutBoxes([]detection{cutBox}, nil))
}
