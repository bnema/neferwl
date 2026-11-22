package vulkan

import (
	"image"
	"math"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/stretchr/testify/require"
)

func tabScene(long string) domain.Scene {
	bar := domain.Rect{X: 0, Y: 0, W: 240, H: 21}
	return domain.Scene{Scale: 1, Border: domain.Border{Active: "#3366ff", Inactive: "#222222"},
		TabBars: []domain.TabBar{{Rect: bar, Tabs: []domain.TabTitle{
			{Rect: domain.Rect{W: 80, H: 21}, Title: "foot", Window: 1},
			{Rect: domain.Rect{X: 80, W: 80, H: 21}, Title: long, Active: true, Window: 2},
			{Rect: domain.Rect{X: 160, W: 80, H: 21}, Title: "mpv", Window: 3},
		}}}}
}

// The bar is one Border.Inactive fill, the active tab a Border.Active
// fill, then one text quad per tab; a long title is cut to the tab minus
// its padding; an unchanged scene reuses the rasters and allocates no more
// than a scene without bars.
func TestSceneTabBars(t *testing.T) {
	r := newTestRenderer(t, 256, 64)
	defer r.Close()
	bounds := image.Rect(0, 0, 256, 64)
	s := tabScene("a very long window title that cannot fit")
	got := r.draws(s, nil, newDamage(&target{}, s, bounds))
	require.Len(t, got, 5, "bar, active fill, 3 titles")
	bar, active := got[0], got[2]
	require.Nil(t, bar.shm)
	require.Equal(t, [4]int32{0, 0, 240, 21}, bar.pc.rect)
	require.Equal(t, uint8(0x22), uint8(bar.pc.color[0]*255+0.5), "bar in Border.Inactive")
	require.Nil(t, active.shm)
	require.Equal(t, [4]int32{80, 0, 160, 21}, active.pc.rect, "active tab fill")
	titles := []draw{got[1], got[3], got[4]}
	for i, tt := range titles {
		require.NotNil(t, tt.shm, "title %d", i)
	}
	long := titles[1]
	require.Equal(t, int32(80+6), long.pc.rect[0], "left aligned after 6 px")
	require.LessOrEqual(t, long.pc.rect[2]-long.pc.rect[0], int32(80-12), "cut to the tab minus 2x6 px")

	entries := r.overlayRaster.Len()
	again := r.draws(s, nil, newDamage(&target{}, s, bounds))
	require.Same(t, got[3].shm, again[3].shm, "same GPU copy")
	require.Equal(t, entries, r.overlayRaster.Len(), "no new raster")

	bare := s
	bare.TabBars = nil
	base := testing.AllocsPerRun(50, func() { r.draws(bare, nil, newDamage(&target{}, bare, bounds)) })
	if n := testing.AllocsPerRun(50, func() { r.draws(s, nil, newDamage(&target{}, s, bounds)) }); n > base {
		t.Fatalf("tab bars allocated %v times, %v without them", n, base)
	}
}

// A bar with a radius is rounded, and so is its active tab, at most a pill.
func TestSceneTabBarsRounded(t *testing.T) {
	r := newTestRenderer(t, 256, 64)
	defer r.Close()
	bounds := image.Rect(0, 0, 256, 64)
	s := tabScene("b")
	s.TabBars[0].Radius = 30
	got := r.draws(s, nil, newDamage(&target{}, s, bounds))
	require.Len(t, got, 5)
	require.Equal(t, float32(10.5), math.Float32frombits(got[0].radii[0]), "bar capped at half its height")
	require.Equal(t, float32(10.5), math.Float32frombits(got[2].radii[0]), "active tab rounded alike")
}
