package hints

import (
	"context"
	"errors"
	"hash/maphash"
	"image"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/domain"
)

var errOCRUnavailable = errors.New("hints: OCR unavailable")

type ocrSource struct {
	state ocrState
	cache map[bandKey]bandCache
	seed  maphash.Seed
}

func (*ocrSource) kind() domain.HintSource               { return domain.HintSourceOCR }
func (s *ocrSource) needsFrame(ctx context.Context) bool { return ocrReady(ctx, &s.state) }
func (*ocrSource) Close() error                          { return nil }

//hexcheck:ignore suspicious-business-logic-in-adapter Owns OCR buffers, cache and external recognition; translates pixel results.
func (s *ocrSource) scan(ctx context.Context, req domain.HintScanRequest, wins []domain.HintWindow, fr *frame) (scanResult, error) {
	if !ocrReady(ctx, &s.state) {
		return scanResult{}, errOCRUnavailable
	}
	if s.seed == (maphash.Seed{}) {
		s.seed = maphash.MakeSeed()
	}
	next := make(map[bandKey]bandCache)
	var jobs []*bandJob
	crops := make([]image.Rectangle, len(wins))
	need, pixels := 0, 0
	for wi, win := range wins {
		r := windowCrop(win.Bounds(), req.Scale, req.Width, req.Height)
		crops[wi] = r
		if r.Empty() {
			continue
		}
		pixels += r.Dx() * r.Dy()
		for _, b := range bandsOf(r.Dy()) {
			ext := image.Rect(0, b.top, r.Dx(), b.bottom)
			j := &bandJob{win: wi, ext: ext, own: [2]int{b.ownTop, b.ownBottom}, key: bandKey{output: req.Output, window: win.ID, band: b.idx, bands: b.n}}
			j.hash = cropHash(s.seed, fr.pix, fr.stride, ext.Add(r.Min))
			entry, ok := s.cache[j.key]
			if ok && entry.hash == j.hash && entry.ext == ext {
				j.hit, j.words = true, entry.words
			} else {
				need += ext.Dx() * ext.Dy()
			}
			jobs = append(jobs, j)
		}
	}
	// The arena is scan-local, while cached words contain no pixel references.
	arenaOwner := worker{}
	var arena *mapping
	if need > 0 {
		arena = arenaOwner.alloc(need)
	}
	defer arena.unmap()
	off := 0
	for _, j := range jobs {
		if !j.hit {
			n := j.ext.Dx() * j.ext.Dy()
			j.gray = cropGray(fr.pix, fr.stride, j.ext.Add(crops[j.win].Min), arena.b[off:off+n])
			off += n
		}
	}
	recognize(ctx, &s.state, jobs)
	var out []domain.HintTarget
	var scanErr error
	hits := 0
	for wi, win := range wins {
		var words []OCRWord
		failed := false
		for _, j := range jobs {
			if j.win != wi {
				continue
			}
			if j.hit {
				hits++
			}
			if j.err != nil {
				failed = true
				scanErr = errors.Join(scanErr, j.err)
				failureWarning.Do(func() {
					log := logging.For(ctx, "hints")
					log.Warn().Err(j.err).Msg("OCR recognition failed")
				})
				continue
			}
			next[j.key] = bandCache{hash: j.hash, ext: j.ext, words: j.words}
			for _, word := range j.words {
				word.Rect = word.Rect.Add(crops[wi].Min)
				if word.Underlined {
					word.UnderR += crops[wi].Min.X
				}
				if !duplicateWord(words, word) {
					words = append(words, word)
				}
			}
		}
		if failed {
			continue
		}
		for _, t := range textLinks(words, req.Scale) {
			if win.Contains(t.Rect.X+t.Rect.W/2, t.Rect.Y+t.Rect.H/2) {
				t.Window = win.ID
				out = append(out, t)
			}
		}
	}
	s.cache = next
	log := logging.For(ctx, "hints")
	log.Debug().Int("ocr_px", pixels).Int("ocr_bands", len(jobs)).Int("band_hits", hits).Msg("hint OCR scan")
	return targetCoverage(out), scanErr
}
