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
