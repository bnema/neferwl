package vulkan

import (
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/overlaydraw"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/font/gofont/goregular"
)

// A font change redraws a target whole, even for the scene it holds; the
// next frame holds it again.
func TestDamageFullAfterFontChange(t *testing.T) {
	t.Cleanup(func() { _ = overlaydraw.LoadFont("") })
	s := domain.Scene{Seq: 3, Scale: 1, Font: "a"}
	bounds := image.Rect(0, 0, 64, 64)
	tg := &target{valid: true, sceneSeq: 3, scene: s, fontGen: overlaydraw.FontGen()}
	require.False(t, newDamage(tg, s, bounds).all(), "same scene, same font: held")

	path := filepath.Join(t.TempDir(), "goregular.ttf")
	require.NoError(t, os.WriteFile(path, goregular.TTF, 0o600))
	require.NoError(t, overlaydraw.LoadFont(path))
	require.True(t, newDamage(tg, s, bounds).all(), "font changed: full")

	tg.fontGen = overlaydraw.FontGen()
	require.False(t, newDamage(tg, s, bounds).all(), "redrawn in the new font: held")

	// Core gives a font change a new scene (SameAs), so outputs draw it.
	o := s
	o.Font = "b"
	require.False(t, s.SameAs(o))
}

// loadTestFont swaps the compositor font to Go Regular until t ends.
func loadTestFont(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { _ = overlaydraw.LoadFont("") })
	path := filepath.Join(t.TempDir(), "goregular.ttf")
	require.NoError(t, os.WriteFile(path, goregular.TTF, 0o600))
	require.NoError(t, overlaydraw.LoadFont(path))
}

// The frame reads the font generation once, when its damage starts: a
// font swapped while it draws leaves the target holding the old one, so
// the next frame redraws in full.
func TestDamageFontSnapshotAtFrameStart(t *testing.T) {
	r := &Renderer{width: 64, height: 64}
	s := domain.Scene{Seq: 3, Scale: 1}
	bounds := image.Rect(0, 0, 64, 64)
	tg := &target{}
	d := newDamage(tg, s, bounds)
	gen := d.fontGen
	loadTestFont(t)
	r.draws(s, nil, d)
	tg.hold(s, d)
	require.Equal(t, gen, tg.fontGen, "held with the frame's generation")
	require.True(t, newDamage(tg, s, bounds).all(), "the next frame redraws in the new font")
}

// A font change between two frames recopies the compositor text whose
// raster keeps its content: tab titles, and an OSD panel.
func TestFontChangeRecopiesText(t *testing.T) {
	r := newTestRenderer(t, 256, 64)
	s := tabScene("b")
	s.TabBars[0].Tabs = s.TabBars[0].Tabs[:1]
	s.TabBars[0].Tabs[0].Title = "x"
	s.Overlays = []domain.Overlay{{ID: 42, Seq: 1, Kind: domain.OverlayPanel, Anchor: domain.AnchorTopCenter,
		Panel: &domain.PanelContent{Lines: [4]string{"p"}}}}
	s.Seq = 1
	require.NoError(t, render(r, s, nil))
	loadTestFont(t)
	gen := overlaydraw.FontGen()
	s.Seq++
	require.NoError(t, render(r, s, nil))
	for k, surface := range r.shm {
		held := false
		for _, b := range surface.bufs {
			held = held || (b != nil && b.valid && b.holds.windowSeq == gen)
		}
		require.True(t, held, "copy %x in the new font", k.overlay)
	}
	require.Len(t, r.shm, 2)
}

// Reloading the same font file changes only the generation: every
// compositor text copy (tab title, drop label, panel) keeps its size and
// content and is still recopied in the new generation.
func TestSameSizeFontChangeRecopiesText(t *testing.T) {
	r := newTestRenderer(t, 256, 64)
	s := tabScene("b")
	s.TabBars[0].Tabs = s.TabBars[0].Tabs[:1]
	s.TabBars[0].Tabs[0].Title = "x"
	s.Overlays = []domain.Overlay{{ID: 42, Seq: 1, Kind: domain.OverlayPanel, Anchor: domain.AnchorTopCenter,
		Panel: &domain.PanelContent{Lines: [4]string{"p"}}}}
	s.DropPreview = &domain.DropPreview{Rect: domain.Rect{X: 10, Y: 10, W: 200, H: 40}, Fill: 0.18, Label: "stack", LabelPx: 13}
	loadTestFont(t)
	s.Seq = 1
	require.NoError(t, render(r, s, nil))
	require.Len(t, r.shm, 3)
	loadTestFont(t)
	gen := overlaydraw.FontGen()
	s.Seq++
	require.NoError(t, render(r, s, nil))
	for k, surface := range r.shm {
		held := false
		for _, b := range surface.bufs {
			held = held || (b != nil && b.valid && b.holds.windowSeq == gen)
		}
		require.True(t, held, "copy %x in the new font", k.overlay)
	}
}
