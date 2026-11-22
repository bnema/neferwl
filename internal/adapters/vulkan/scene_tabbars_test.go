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
		TabBars: []domain.TabBar{{Rect: bar, FontPx: 13, Tabs: []domain.TabTitle{
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

	entries := r.tabTitles.Len()
	copy3 := got[3].shm // draws reuses its slice
	again := r.draws(s, nil, newDamage(&target{}, s, bounds))
	require.Same(t, copy3, again[3].shm, "same GPU copy")
	require.Equal(t, entries, r.tabTitles.Len(), "no new raster")

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

// shift moves the bars of s by dx logical px and widens every tab by dw.
func shift(s domain.Scene, dx, dw int) domain.Scene {
	bar := s.TabBars[0]
	bar.Rect.X += dx
	bar.Rect.W += 3 * dw
	tabs := append([]domain.TabTitle(nil), bar.Tabs...)
	for i := range tabs {
		tabs[i].Rect.X += dx + i*dw
		tabs[i].Rect.W += dw
	}
	bar.Tabs = tabs
	s.TabBars = []domain.TabBar{bar}
	return s
}

// A bar moving by a logical px at a fractional scale, or a width spring
// changing its tabs by a few px, keeps every title's GPU copy and raster,
// without allocating.
func TestSceneTabBarsMoveKeepsTitles(t *testing.T) {
	r := newTestRenderer(t, 400, 64)
	defer r.Close()
	bounds := image.Rect(0, 0, 400, 64)
	s := tabScene("a very long window title that cannot fit")
	s.Scale = 1.25
	first := r.draws(s, nil, newDamage(&target{}, s, bounds))
	require.Len(t, first, 5)
	// draws reuses its slice: keep the copies.
	copies := []*shmCopy{first[1].shm, first[3].shm, first[4].shm}
	entries := r.tabTitles.Len()
	for _, step := range []struct{ dx, dw int }{{1, 0}, {2, 0}, {0, 2}, {1, 3}} {
		m := shift(s, step.dx, step.dw)
		got := r.draws(m, nil, newDamage(&target{}, m, bounds))
		require.Len(t, got, 5)
		for k, i := range []int{1, 3, 4} {
			require.Same(t, copies[k], got[i].shm, "step %+v title %d", step, i)
		}
		require.Equal(t, entries, r.tabTitles.Len())
	}
	a, b := shift(s, 1, 0), shift(s, 0, 2)
	base := testing.AllocsPerRun(20, func() {
		r.draws(a, nil, newDamage(&target{}, a, bounds))
	})
	if n := testing.AllocsPerRun(20, func() {
		r.draws(a, nil, newDamage(&target{}, a, bounds))
		r.draws(b, nil, newDamage(&target{}, b, bounds))
	}); n > 2*base {
		t.Fatalf("moving bars allocate %v, %v per steady draw", n, base)
	}
}

// The damage of bars covers a bar added, a bar removed and a radius-only
// change, and nothing when the bars are the same.
func TestTabBarDelta(t *testing.T) {
	phys := func(r domain.Rect) image.Rectangle { return image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H) }
	a := domain.TabBar{Rect: domain.Rect{X: 0, Y: 0, W: 100, H: 20}, FontPx: 12}
	b := domain.TabBar{Rect: domain.Rect{X: 200, Y: 0, W: 50, H: 20}, FontPx: 12}
	rounded := a
	rounded.Radius = 8
	for _, tc := range []struct {
		name     string
		old, cur []domain.TabBar
		want     image.Rectangle
	}{
		{"same", []domain.TabBar{a, b}, []domain.TabBar{a, b}, image.Rectangle{}},
		{"added", []domain.TabBar{a}, []domain.TabBar{a, b}, phys(b.Rect)},
		{"removed", []domain.TabBar{a, b}, []domain.TabBar{a}, phys(b.Rect)},
		{"radius", []domain.TabBar{a, b}, []domain.TabBar{rounded, b}, phys(a.Rect)},
	} {
		if got := tabBarDelta(tc.old, tc.cur, phys); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
