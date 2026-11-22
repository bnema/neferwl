package vulkan

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/overlaydraw"
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

// A bar with a radius is the visible tab's title bar: top corners rounded
// at most its height, bottom square. Its active tab is rounded only on the
// corners it shares with the bar; the visible tab window below it loses its
// top rounding (border ring and content clip), other windows keep theirs.
func TestSceneTabBarsRounded(t *testing.T) {
	r := newTestRenderer(t, 256, 128)
	defer r.Close()
	bounds := image.Rect(0, 0, 256, 128)
	s := tabScene("b")
	s.TabBars[0].Radius = 30
	got := r.draws(s, nil, newDamage(&target{}, s, bounds))
	require.Len(t, got, 5)
	require.Equal(t, float32(21), math.Float32frombits(got[0].radii[0]), "bar capped at its height")
	require.Equal(t, cornersBottom, got[0].square, "bar bottom square")
	require.Equal(t, float32(21), math.Float32frombits(got[2].radii[0]), "active tab rounded alike")
	require.Equal(t, cornersTop|cornersBottom, got[2].square, "middle tab square")

	for _, c := range []struct {
		active int
		want   uint32
	}{{0, cornerTopRight | cornersBottom}, {2, cornerTopLeft | cornersBottom}} {
		e := tabScene("b")
		e.TabBars[0].Radius = 30
		tabs := append([]domain.TabTitle(nil), e.TabBars[0].Tabs...)
		for i := range tabs {
			tabs[i].Active = i == c.active
		}
		e.TabBars[0].Tabs = tabs
		got := r.draws(e, nil, newDamage(&target{}, e, bounds))
		at := 1 + c.active // the bar, the titles before it
		require.Nil(t, got[at].shm)
		require.Equal(t, c.want, got[at].square, "active tab %d", c.active)
	}

	// The visible tab right under the bar, and a window elsewhere.
	s.Border.Width = 2
	s.Windows = []domain.SceneWindow{
		{ID: 2, Rect: domain.Rect{X: 0, Y: 21, W: 240, H: 62}, Radius: 12, Focused: true},
		{ID: 4, Rect: domain.Rect{X: 0, Y: 90, W: 100, H: 30}, Radius: 12},
	}
	got = r.draws(s, nil, newDamage(&target{}, s, bounds))
	tab, other := 0, 0
	for _, d := range got {
		if d.pc.misc[1]&flagRounded == 0 || d.square == cornersBottom || d.round[1] < 21 {
			continue
		}
		if d.round[1] < 90 {
			require.Equal(t, cornersTop, d.square, "tab draw %+v", d.round)
			tab++
		} else {
			require.Zero(t, d.square, "other window draw %+v", d.round)
			other++
		}
	}
	require.NotZero(t, tab, "tab window draws")
	require.NotZero(t, other, "other window draws")
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

// A bar whose Border.Inactive is (nearly) the background is Border.Active
// over the background at 25 %; a distinct one is kept. Its inactive title
// ink keeps the contrast against that fill.
func TestTabBarFill(t *testing.T) {
	bg, active := [3]uint8{0x11, 0x11, 0x11}, [3]uint8{0x99, 0x99, 0x99}
	for _, c := range []struct {
		inactive, want [3]uint8
	}{
		{bg, [3]uint8{0x33, 0x33, 0x33}},
		{[3]uint8{0x13, 0x10, 0x14}, [3]uint8{0x33, 0x33, 0x33}},
		{[3]uint8{0x22, 0x22, 0x22}, [3]uint8{0x22, 0x22, 0x22}},
	} {
		got := tabBarFill(c.inactive, bg, active)
		require.Equal(t, c.want, got, "inactive %v", c.inactive)
		fill := color.RGBA{got[0], got[1], got[2], 255}
		require.Equal(t, color.RGBA{192, 192, 192, 255}, overlaydraw.MutedInk(fill))
	}

	// Drawn: the default inactive bar on the default background.
	r := newTestRenderer(t, 256, 64)
	defer r.Close()
	s := tabScene("b")
	s.Background, s.Border.Active, s.Border.Inactive = "#111111", "#999999", "#111111"
	got := r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 256, 64)))
	require.Equal(t, uint8(0x33), uint8(got[0].pc.color[0]*255+0.5), "visible bar")
}

// ringedTab is tabScene's bar with radius 12 over its visible tab (window
// 2, right under it) with gaps: the window owns its four border lines and
// its content touches the bar.
func ringedTab(active bool) domain.Scene {
	s := tabScene("b")
	s.TabBars[0].Radius = 12
	s.Border.Width, s.Border.Radius = 2, 12
	win := domain.Rect{X: 0, Y: 21, W: 240, H: 62}
	s.Windows = []domain.SceneWindow{{ID: 2, Rect: win, Radius: 12, Inset: domain.SideAll &^ domain.SideTop, Focused: active}}
	for _, side := range []domain.Sides{domain.SideLeft, domain.SideRight, domain.SideTop, domain.SideBottom} {
		r := win
		switch side {
		case domain.SideLeft:
			r.W = 2
		case domain.SideRight:
			r.X, r.W = win.X+win.W-2, 2
		case domain.SideTop:
			r.H = 2
		case domain.SideBottom:
			r.Y, r.H = win.Y+win.H-2, 2
		}
		s.Separators = append(s.Separators, domain.Separator{Rect: r, Active: active, Window: 2})
	}
	return s
}

// A visible tab's ring encloses its bar: drawn once around the union, four
// corners rounded, its hole up to the bar's inner top; the bar fill sits
// inside the ring and the window keeps its square-top content clip.
func TestSceneTabRingEnclosesBar(t *testing.T) {
	r := newTestRenderer(t, 256, 128)
	defer r.Close()
	bounds := image.Rect(0, 0, 256, 128)
	s := ringedTab(true)
	got := r.draws(s, nil, newDamage(&target{}, s, bounds))
	rings := 0
	for _, d := range got {
		if d.pc.misc[1]&flagRing == 0 {
			continue
		}
		rings++
		require.Equal(t, [4]int32{0, 0, 240, 83}, d.pc.rect, "ring around bar and window")
		require.Zero(t, d.square, "four rounded corners")
		require.Equal(t, float32(12), math.Float32frombits(d.radii[0]))
		require.Equal(t, float32(2+1), d.roundInner[1], "hole from the bar's inner top")
		require.Equal(t, float32(12-2-1), math.Float32frombits(d.radii[1]), "hole radius concentric with the ring")
		require.Equal(t, uint8(0x33), uint8(d.pc.color[0]*255+0.5), "active colour")
	}
	require.Equal(t, 1, rings)
	var bar *draw
	for i := range got {
		if got[i].square == cornersBottom && got[i].shm == nil {
			bar = &got[i]
			break
		}
	}
	require.NotNil(t, bar)
	require.Equal(t, [4]int32{2, 2, 238, 21}, bar.pc.rect, "bar inside the ring")
	require.Equal(t, float32(10), math.Float32frombits(bar.radii[0]), "inner radius")
	body := 0
	for _, d := range got {
		if d.pc.misc[1]&flagRing == 0 && d.round[1] == 21 && d.pc.misc[1]&flagRounded != 0 {
			require.Equal(t, cornersTop, d.square, "content clip square on top")
			require.Equal(t, float32(12-2), math.Float32frombits(d.radii[0]), "body radius concentric with the ring")
			body++
		}
	}
	require.NotZero(t, body, "content from the bar's bottom")

	// No more allocations than the same scene without the ring.
	bare := s
	bare.Separators = nil
	base := testing.AllocsPerRun(50, func() { r.draws(bare, nil, newDamage(&target{}, bare, bounds)) })
	if n := testing.AllocsPerRun(50, func() { r.draws(s, nil, newDamage(&target{}, s, bounds)) }); n > base {
		t.Fatalf("ring allocated %v times, %v without it", n, base)
	}
}

// A focus change on the visible tab damages its bar with the window.
func TestTabRingFocusDamagesBar(t *testing.T) {
	phys := func(r domain.Rect) image.Rectangle { return image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H) }
	area, ok := sceneDelta(ringedTab(false), ringedTab(true), phys, 1<<30)
	require.True(t, ok)
	require.Equal(t, image.Rect(0, 0, 240, 83), area)
}

// A window right under a bar but not its active tab (a flash or a move
// can leave it there) is no visible tab: four rounded corners, no ring
// around the bar, no bar damage.
func TestTabRingNeedsTheActiveTab(t *testing.T) {
	r := newTestRenderer(t, 256, 128)
	bounds := image.Rect(0, 0, 256, 128)
	s := ringedTab(true)
	s.Windows[0].ID = 3
	for i := range s.Separators {
		s.Separators[i].Window = 3
	}
	for _, d := range r.draws(s, nil, newDamage(&target{}, s, bounds)) {
		require.NotEqual(t, cornersTop, d.square, "draw %+v", d.pc.rect)
		if d.pc.misc[1]&flagRing != 0 {
			require.Equal(t, int32(21), d.pc.rect[1], "ring around the window only")
		}
	}
	phys := func(r domain.Rect) image.Rectangle { return image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H) }
	old := s
	old.Windows = []domain.SceneWindow{s.Windows[0]}
	old.Windows[0].Focused = false
	area, ok := sceneDelta(old, s, phys, 1<<30)
	require.True(t, ok)
	require.Equal(t, image.Rect(0, 21, 240, 83), area)
}
