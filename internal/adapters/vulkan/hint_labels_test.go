package vulkan

import (
	"image"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bnema/neferwl/internal/ports"
)

func TestHintLabelsDrawnAfterIndicators(t *testing.T) {
	r, err := New(256, 256)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	for _, scale := range []int{1, 2} {
		s := ports.Scene{Scale: float64(scale),
			CaptureIndicators: []ports.CaptureIndicator{{Rect: ports.Rect{W: 64, H: 64}}},
			HintLabels: []ports.HintLabel{
				{Rect: ports.Rect{X: 80, Y: 90}, Text: "f"},
				{Rect: ports.Rect{X: 100, Y: 110}, Text: "jk", Typed: 1},
			}}
		bare := s
		bare.HintLabels = nil
		before := len(r.draws(bare, nil, newDamage(&target{}, bare, image.Rect(0, 0, 256, 256))))
		ds := r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 256, 256)))
		require.Len(t, ds, before+2, "one draw per label, after the indicator's")
		for i, tl := range []image.Point{{80 * scale, 90 * scale}, {100 * scale, 110 * scale}} {
			d := ds[before+i]
			require.NotNil(t, d.shm)
			lw, lh := int(d.pc.rect[2]-d.pc.rect[0]), int(d.pc.rect[3]-d.pc.rect[1])
			inset := int(math.Round(0.25 * float64(lh)))
			require.Equal(t, int32(tl.X-lw+inset), d.pc.rect[0])
			require.Equal(t, int32(tl.Y-lh+inset), d.pc.rect[1])
			require.Greater(t, d.pc.rect[2], d.pc.rect[0])
			require.Greater(t, d.pc.rect[3], d.pc.rect[1])
			require.Equal(t, float32(1), d.pc.color[3])
		}
	}
}

func TestHintLabelsAnchoredAndClampedAtTopLeft(t *testing.T) {
	r, err := New(128, 128)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	// A target at the origin puts the anchor off-screen: clamped to 0,0.
	s := ports.Scene{Scale: 1, HintLabels: []ports.HintLabel{{Rect: ports.Rect{X: 0, Y: 0}, Text: "f"}, {Rect: ports.Rect{X: 60, Y: 60}, Text: "f"}}}
	ds := r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 128, 128)))
	require.Len(t, ds, 2)
	require.Equal(t, int32(0), ds[0].pc.rect[0])
	require.Equal(t, int32(0), ds[0].pc.rect[1])
	lw, lh := int(ds[1].pc.rect[2]-ds[1].pc.rect[0]), int(ds[1].pc.rect[3]-ds[1].pc.rect[1])
	inset := int(math.Round(0.25 * float64(lh)))
	require.Equal(t, int32(60-lw+inset), ds[1].pc.rect[0])
	require.Equal(t, int32(60-lh+inset), ds[1].pc.rect[1])
	require.Less(t, int(ds[1].pc.rect[2]), 60+lw, "label sits left of the target, mostly outside it")
}

func TestHintLabelsAbsentDrawNothing(t *testing.T) {
	r := &Renderer{width: 64, height: 64}
	s := ports.Scene{Scale: 1}
	require.Empty(t, r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 64, 64))))
}

func TestHintLabelsClampedInsideOutput(t *testing.T) {
	r, err := New(128, 128)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	s := ports.Scene{Scale: 1, HintStyle: ports.HintStyle{Size: 40}, HintLabels: []ports.HintLabel{{Rect: ports.Rect{X: 127, Y: 127}, Text: "ff"}}}
	ds := r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 128, 128)))
	require.Len(t, ds, 1)
	require.LessOrEqual(t, ds[0].pc.rect[2], int32(128))
	require.LessOrEqual(t, ds[0].pc.rect[3], int32(128))
}

func TestHintLabelsRenderAndUploadOnce(t *testing.T) {
	r, err := New(128, 128)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	s := ports.Scene{Scale: 1, Seq: 1, HintLabels: []ports.HintLabel{{Rect: ports.Rect{X: 4, Y: 4}, Text: "fj"}}}
	require.NoError(t, render(r, s, nil))
	first := r.copied
	require.Positive(t, first)
	s.Seq = 2
	require.NoError(t, render(r, s, nil))
	require.Equal(t, first, r.copied, "an unchanged label is not uploaded again")
	s.HintStyle.Size = 26
	s.Seq++
	require.NoError(t, render(r, s, nil))
	require.Greater(t, r.copied, first, "size reload uploads a new raster")
	sized := r.copied
	s.HintStyle.Background = "#89b4fa"
	s.Seq++
	require.NoError(t, render(r, s, nil))
	require.Greater(t, r.copied, sized, "colour reload uploads a new raster")
}

func TestHintSceneDeltaOnlyHintLabelsChange(t *testing.T) {
	phys := func(r ports.Rect) image.Rectangle { return image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H) }
	base := ports.Scene{Scale: 1, OutputWidth: 100, OutputHeight: 100}
	with := base
	with.HintLabels = []ports.HintLabel{{Rect: ports.Rect{X: 1, Y: 1}, Text: "f"}}
	_, ok := sceneDelta(base, with, phys, 1<<30)
	require.False(t, ok, "labels appearing redraw the output")
	typed := with
	typed.HintLabels = []ports.HintLabel{{Rect: ports.Rect{X: 1, Y: 1}, Text: "f", Typed: 1}}
	_, ok = sceneDelta(with, typed, phys, 1<<30)
	require.False(t, ok)
	_, ok = sceneDelta(with, with, phys, 1<<30)
	require.True(t, ok, "same labels, no damage")
}

func TestHintLabelsHeldByTarget(t *testing.T) {
	s := ports.Scene{Seq: 1, HintLabels: []ports.HintLabel{{Text: "f"}}}
	tg := &target{}
	tg.hold(s, &damageRegion{drawn: map[ports.WindowID]heldWindow{}})
	s.HintLabels[0].Text = "j"
	require.Equal(t, "f", tg.scene.HintLabels[0].Text, "the held scene owns its labels")
}

func TestHintStyleChangesSceneDamage(t *testing.T) {
	old := ports.Scene{Scale: 1, HintLabels: []ports.HintLabel{{Text: "f"}}}
	changed := old
	changed.HintStyle.Size = 24
	require.False(t, old.SameAs(changed))
	_, partial := sceneDelta(old, changed, func(r ports.Rect) image.Rectangle { return image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H) }, 10)
	require.False(t, partial)
}
