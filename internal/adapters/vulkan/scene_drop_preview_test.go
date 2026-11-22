package vulkan

import (
	"image"
	"math"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/stretchr/testify/require"
)

func dropScene(p *domain.DropPreview, ws ...domain.SceneWindow) domain.Scene {
	return domain.Scene{
		Scale:       1,
		Border:      domain.Border{Active: "#ff0000"},
		Windows:     append([]domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 40, H: 60}}}, ws...),
		Layers:      []domain.SceneLayer{{ID: 9, Layer: domain.LayerTop, Rect: domain.Rect{W: 80, H: 5}}},
		DropPreview: p,
	}
}

func isRed(d draw) bool { return d.pc.color[0] > 0 && d.pc.color[1] == 0 && d.pc.color[2] == 0 }

// The zone is one translucent fill at Fill, rounded with Radius, then an
// opaque ring, after the windows and before the top layers; nothing
// without a preview.
func TestSceneDropPreviewZoneAndRing(t *testing.T) {
	r := &Renderer{width: 80, height: 60}
	bounds := image.Rect(0, 0, 80, 60)
	bare := dropScene(nil)
	base := len(r.draws(bare, nil, newDamage(&target{}, bare, bounds)))

	s := dropScene(&domain.DropPreview{Rect: domain.Rect{X: 10, Y: 10, W: 40, H: 30}, Radius: 6, Fill: 0.18})
	got := r.draws(s, nil, newDamage(&target{}, s, bounds))
	// tile, zone, ring (the top layer has no content).
	require.Len(t, got, base+2)
	zone, ring := got[1], got[2]
	require.True(t, isRed(zone) && isRed(ring))
	require.Equal(t, [4]int32{10, 10, 50, 40}, zone.pc.rect)
	require.InDelta(t, 0.18, zone.pc.color[3], 1e-6, "zone at its Fill alpha")
	require.Equal(t, float32(6), math.Float32frombits(zone.radii[0]), "rounded with Radius")
	require.Zero(t, zone.pc.misc[1]&flagRing)
	require.Equal(t, float32(1), ring.pc.color[3], "the ring is opaque")
	require.NotZero(t, ring.pc.misc[1]&flagRing)
	require.Equal(t, [4]float32{12, 12, 48, 38}, ring.roundInner, "2 px ring")

	// Square: a plain fill and four strips.
	s.DropPreview = &domain.DropPreview{Rect: domain.Rect{X: 10, Y: 10, W: 40, H: 30}, Fill: 0.18}
	require.Len(t, r.draws(s, nil, newDamage(&target{}, s, bounds)), base+5)
	// Empty zone: nothing.
	s.DropPreview = &domain.DropPreview{Rect: domain.Rect{X: 10, H: 30}, Fill: 0.18}
	require.Len(t, r.draws(s, nil, newDamage(&target{}, s, bounds)), base)
}

// The label pill is centred in the zone when it fits inside it minus
// 8 px, and skipped otherwise; a cached label draws without allocating.
func TestSceneDropPreviewLabel(t *testing.T) {
	r := newTestRenderer(t, 256, 256)
	defer r.Close()
	bounds := image.Rect(0, 0, 256, 256)
	p := &domain.DropPreview{Rect: domain.Rect{X: 20, Y: 20, W: 200, H: 120}, Fill: 0.18, Label: "stack", LabelPx: 13}
	s := domain.Scene{Scale: 1, Border: domain.Border{Active: "#3366ff"}, DropPreview: p}
	got := r.draws(s, nil, newDamage(&target{}, s, bounds))
	require.Len(t, got, 6, "zone, 4 ring strips, label")
	l := got[5]
	require.NotNil(t, l.shm)
	lw, lh := l.pc.rect[2]-l.pc.rect[0], l.pc.rect[3]-l.pc.rect[1]
	require.Equal(t, int32(20+(200-lw)/2), l.pc.rect[0])
	require.Equal(t, int32(20+(120-lh)/2), l.pc.rect[1])
	require.Greater(t, lh, int32(13+2*6), "6 px padding")

	// The walk's own setup allocates the same with or without a preview:
	// the cached label and the zone add nothing.
	bare := s
	bare.DropPreview = nil
	base := testing.AllocsPerRun(50, func() { r.draws(bare, nil, newDamage(&target{}, bare, bounds)) })
	if n := testing.AllocsPerRun(50, func() { r.draws(s, nil, newDamage(&target{}, s, bounds)) }); n > base {
		t.Fatalf("preview draw allocated %v times, %v without it", n, base)
	}

	// Another label is another copy.
	s.DropPreview = &domain.DropPreview{Rect: p.Rect, Fill: 0.18, Label: "swap", LabelPx: 13}
	other := r.draws(s, nil, newDamage(&target{}, s, bounds))
	require.Len(t, other, 6)
	require.NotEqual(t, lw, other[5].pc.rect[2]-other[5].pc.rect[0])

	// Too small: no pill.
	s.DropPreview = &domain.DropPreview{Rect: domain.Rect{X: 20, Y: 20, W: int(lw) + 15, H: 120}, Fill: 0.18, Label: "stack", LabelPx: 13}
	require.Len(t, r.draws(s, nil, newDamage(&target{}, s, bounds)), 5)
}

// A Proxy window is skipped by the window pass and drawn once, after the
// zone, at its ThumbRect, with an outline.
func TestSceneDropPreviewProxy(t *testing.T) {
	r := &Renderer{width: 80, height: 60}
	bounds := image.Rect(0, 0, 80, 60)
	thumb := domain.Rect{X: 50, Y: 30, W: 24, H: 18}
	proxy := domain.SceneWindow{ID: 7, Proxy: true, Rect: thumb, Zoom: 0.25}
	s := dropScene(&domain.DropPreview{Rect: domain.Rect{X: 2, Y: 2, W: 30, H: 30}, Fill: 0.18, Thumb: 7, ThumbRect: thumb}, proxy)
	s.Border.Width = 3
	got := r.draws(s, nil, newDamage(&target{}, s, bounds))
	at := -1
	for i, d := range got {
		if d.pc.rect == [4]int32{50, 30, 74, 48} && !isRed(d) {
			require.Equal(t, -1, at, "proxy drawn twice")
			at = i
		}
	}
	// tile, zone fill, 4 ring strips, proxy body (no border), 4 outline strips (the top layer has no content).
	require.Equal(t, 6, at, "proxy after the zone: %+v", got)
	for _, d := range got[at+1 : at+5] {
		require.True(t, isRed(d))
		require.True(t, image.Rect(int(d.pc.rect[0]), int(d.pc.rect[1]), int(d.pc.rect[2]), int(d.pc.rect[3])).In(image.Rect(50, 30, 74, 48)))
	}
	require.Len(t, got, at+5)

	// Without a preview naming it, a Proxy draws nothing.
	s.DropPreview = nil
	bare := dropScene(nil)
	require.Len(t, r.draws(s, nil, newDamage(&target{}, s, bounds)), len(r.draws(bare, nil, newDamage(&target{}, bare, bounds))))
}
