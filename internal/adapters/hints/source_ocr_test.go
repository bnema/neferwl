package hints

import (
	"hash/maphash"
	"image"
	"path/filepath"
	"runtime"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/stretchr/testify/require"
)

// Seed the cache with synthetic OCR words: recognition has no runner interface.
// An absent executable makes any unexpected recognition attempt fail visibly.
func TestOCRScanCachedBands(t *testing.T) {
	oldWorkers := ocrWorkers
	oldProcs := runtime.GOMAXPROCS(2)
	ocrWorkers = 2
	t.Cleanup(func() { ocrWorkers = oldWorkers; runtime.GOMAXPROCS(oldProcs) })
	tmp := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		req := domain.HintScanRequest{Output: "test", Width: 240, Height: 300, Scale: 2}
		win := domain.HintWindow{ID: 7, Rect: domain.Rect{X: 10, Y: 20, W: 100, H: 100}}
		fr := &frame{pix: make([]byte, req.Width*req.Height*4), stride: req.Width * 4}
		crop := windowCrop(win.Bounds(), req.Scale, req.Width, req.Height)
		bands := bandsOf(crop.Dy())
		require.Len(t, bands, 2)
		require.Greater(t, bands[0].bottom, bands[1].top)
		s := &ocrSource{
			state: ocrState{done: true, binary: filepath.Join(tmp, "missing-tesseract"), lang: "eng"},
			seed:  maphash.MakeSeed(), cache: make(map[bandKey]bandCache),
		}
		// The same overlap word is deliberately present in both entries to exercise
		// scan's defensive dedupe, independently of readBand's ownership filter.
		word := OCRWord{Text: "example.org", Rect: image.Rect(20, 94, 100, 110), Confidence: 90}
		for _, b := range bands {
			ext := image.Rect(0, b.top, crop.Dx(), b.bottom)
			key := bandKey{output: req.Output, window: win.ID, band: b.idx, bands: b.n}
			s.cache[key] = bandCache{hash: cropHash(s.seed, fr.pix, fr.stride, ext.Add(crop.Min)), ext: ext, words: []OCRWord{word}}
		}
		want := []domain.HintTarget{{Window: win.ID, Rect: domain.Rect{X: 20, Y: 67, W: 40, H: 8}, URL: "https://example.org", Kind: domain.HintLink, Source: domain.HintSourceOCR, Score: 1}}
		for range 2 {
			got, err := s.scan(t.Context(), req, []domain.HintWindow{win}, fr)
			require.NoError(t, err, "unchanged bands must not invoke the missing executable")
			require.Equal(t, want, got.Targets)
			require.Equal(t, []domain.WindowID{win.ID}, got.Covered)
			require.Len(t, s.cache, 2)
			for _, entry := range s.cache {
				require.Equal(t, []OCRWord{word}, entry.words, "assembly must not mutate cached coordinates")
			}
		}
		// Change a pixel exclusive to the first band's extent. Only that entry
		// should be invalidated; its recognition failure must not poison the other.
		fr.pix[crop.Min.Y*fr.stride+crop.Min.X*4] = 255
		got, err := s.scan(t.Context(), req, []domain.HintWindow{win}, fr)
		require.Error(t, err, "changed band must attempt recognition")
		require.Contains(t, err.Error(), "missing-tesseract")
		require.Empty(t, got.Targets, "a failed window must not emit partial targets")
		require.Empty(t, got.Covered, "a failed window must not be covered")
		require.Len(t, s.cache, 1)
		key := bandKey{output: req.Output, window: win.ID, band: 1, bands: 2}
		require.Contains(t, s.cache, key, "unchanged second band remains cached")
	})
}
