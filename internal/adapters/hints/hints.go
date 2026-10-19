// Package hints finds clickable targets for hint mode: it captures the last
// frame of an output through the capture pipeline and reads uncovered windows with OCR.
package hints

import (
	"context"
	"errors"
	"fmt"
	"hash/maphash"
	"image"
	"math"
	"strings"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/ports"
)

// ErrBusy answers a request that arrives while another one is in flight.
var ErrBusy = errors.New("hints: scan in progress")

// ErrTimeout is the error of a capture that is not answered in time.
var ErrTimeout = errors.New("hints: capture timed out")

// ErrBadRequest is the error of a request without a usable buffer size.
var ErrBadRequest = errors.New("hints: invalid output size")

const (
	captureTimeout = time.Second
	// formatXRGB8888 is the capture format: opaque BGRA in memory.
	formatXRGB8888 = 1
	maxPixels      = 1 << 28
)

// Run serves requests until ctx is done. It handles one request at a time;
// a request that arrives meanwhile is answered with ErrBusy. Every request
// gets exactly one result on results.
func Run(ctx context.Context, requests <-chan ports.HintScanRequest, captures chan<- ports.CaptureRequest, captured <-chan ports.CaptureDone, results chan<- ports.HintScanResult, opts Options) error {
	return run(ctx, requests, captures, captured, results, captureTimeout, opts)
}

// targetSource finds hint targets in the accessibility tree of windows. It
// returns the targets (logical output-local) and the windows they cover.
type targetSource interface {
	Targets(ctx context.Context, windows []ports.HintWindow, linksOnly bool) ([]ports.HintTarget, map[ports.WindowID]bool, error)
	// Close releases the source. It is called once, by the worker goroutine,
	// when the worker returns; the source is not safe for concurrent use.
	Close() error
}

// atspiResult is what the source found for one request.
type atspiResult struct {
	targets []ports.HintTarget
	covered map[ports.WindowID]bool
	dur     time.Duration
	err     error
}

type worker struct {
	source       targetSource
	cache        map[bandKey]bandCache
	cacheSeed    maphash.Seed
	ocr          ocrState
	warnedSource bool
	n            uint64
	dumpDir      string
	pending      *dumpJob
	// orphans are capture buffers the output may still write: they are
	// released when its CaptureDone finally arrives.
	orphans map[uint64]*mapping
	live    int // mappings not released yet
}

// frame is the captured output: BGRA pixels, nil when no capture was needed.
type frame struct {
	pix    []byte
	stride int
	m      *mapping
}

func run(ctx context.Context, requests <-chan ports.HintScanRequest, captures chan<- ports.CaptureRequest, captured <-chan ports.CaptureDone, results chan<- ports.HintScanResult, timeout time.Duration, opts Options) error {
	w := &worker{dumpDir: opts.DumpDir, source: opts.Source}
	if opts.Source != nil {
		// The worker goroutine is the only one to touch the source.
		defer func() { _ = opts.Source.Close() }()
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case done := <-captured:
			// A capture abandoned by a timeout is finally done: release it.
			w.reap(done.ID)
		case req := <-requests:
			res := w.serve(ctx, req, requests, captures, captured, results, timeout)
			if !send(ctx, results, res) {
				// The dump job will never run: release its frame.
				if j := w.pending; j != nil {
					w.pending = nil
					j.m.unmap()
				}
				return nil
			}
			w.dump(ctx)
		}
	}
}

// dump writes the last successful scan, after its result was sent, then
// releases its frame.
func (w *worker) dump(ctx context.Context) {
	j := w.pending
	w.pending = nil
	if j == nil {
		return
	}
	defer j.m.unmap()
	if err := writeDump(w.dumpDir, j); err != nil {
		log := logging.For(ctx, "hints")
		log.Warn().Err(err).Str("dir", w.dumpDir).Msg("hint debug dump failed")
	}
}

func send(ctx context.Context, results chan<- ports.HintScanResult, res ports.HintScanResult) bool {
	select {
	case results <- res:
		return true
	case <-ctx.Done():
		return false
	}
}

// needFrame reports whether the scan must capture the output: OCR has a
// window to read, or the debug dump wants the frame. With OCR unavailable
// (tesseract missing) and no dump, the accessibility targets are the result.
func (w *worker) needFrame(ctx context.Context, req ports.HintScanRequest, ar atspiResult) bool {
	ocr := w.ocrReady(ctx)
	if w.dumpDir != "" {
		return true
	}
	if !ocr {
		return false
	}
	for _, win := range req.Windows {
		if !ar.covered[win.ID] && !windowCrop(win.Rect, req.Scale, req.Width, req.Height).Empty() {
			return true
		}
	}
	return false
}

func (w *worker) serve(ctx context.Context, req ports.HintScanRequest, requests <-chan ports.HintScanRequest, captures chan<- ports.CaptureRequest, captured <-chan ports.CaptureDone, results chan<- ports.HintScanResult, timeout time.Duration) ports.HintScanResult {
	log := logging.For(ctx, "hints")
	res := ports.HintScanResult{Seq: req.Seq, Output: req.Output}
	fail := func(err error) ports.HintScanResult {
		log.Info().Err(err).Str("output", req.Output).Msg("hint scan failed")
		res.Err = err
		return res
	}

	if req.Width <= 0 || req.Height <= 0 || req.Width*req.Height > maxPixels {
		return fail(fmt.Errorf("%w: %dx%d", ErrBadRequest, req.Width, req.Height))
	}
	// The accessibility query is always waited for: the source is not shared
	// between requests. The capture only starts once it is known whether any
	// window needs OCR.
	atspi := w.startSource(ctx, req)
	defer func() { <-atspi.done }()
	if !w.needFrame(ctx, req, atspi.wait()) {
		return w.scan(ctx, req, frame{}, 0, res, atspi)
	}
	// The capture buffer is anonymous mapped memory, outside the Go heap. It
	// is released once the crops are extracted, or by the dump job.
	m := w.alloc(req.Width * req.Height * 4)
	keep := false // the mapping outlives this call: dump job or pending capture
	defer func() {
		if !keep {
			m.unmap()
		}
	}()
	buf := m.b

	w.n++
	id := ports.CaptureIDInternal | w.n
	stride := req.Width * 4
	start := time.Now()
	creq := ports.CaptureRequest{
		ID:       id,
		Output:   req.Output,
		Region:   image.Rect(0, 0, req.Width, req.Height),
		Mapped:   buf,
		Width:    req.Width,
		Height:   req.Height,
		Stride:   stride,
		Format:   formatXRGB8888,
		Indicate: false,
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case captures <- creq:
	case <-timer.C:
		return fail(ErrTimeout)
	case <-ctx.Done():
		return fail(ctx.Err())
	}
	// From here the output owns buf until CaptureDone.
	abandon := func() {
		if w.orphans == nil {
			w.orphans = map[uint64]*mapping{}
		}
		w.orphans[id] = m
		keep = true
	}

	for {
		select {
		case done := <-captured:
			if done.ID != id {
				w.reap(done.ID)
				continue
			}
			if done.Err != nil {
				return fail(fmt.Errorf("hints: capture: %w", done.Err))
			}
			res = w.scan(ctx, req, frame{pix: buf, stride: stride, m: m}, time.Since(start), res, atspi)
			keep = w.pending != nil && w.pending.m == m
			return res
		case other := <-requests:
			// The buffer is in use until the capture is done: answer now.
			busy := ports.HintScanResult{Seq: other.Seq, Output: other.Output, Err: ErrBusy}
			if !send(ctx, results, busy) {
				abandon()
				return fail(ctx.Err())
			}
		case <-timer.C:
			// The output may still write into buf: it is released when its
			// CaptureDone arrives.
			abandon()
			return fail(ErrTimeout)
		case <-ctx.Done():
			abandon()
			return fail(ctx.Err())
		}
	}
}

// reap releases the buffer of an abandoned capture that is finally done.
func (w *worker) reap(id uint64) {
	if m, ok := w.orphans[id]; ok {
		delete(w.orphans, id)
		m.unmap()
	}
}

// bandJob is one OCR band of one window crop.
type bandJob struct {
	win   int             // index into the scan's windows
	key   bandKey         //
	ext   image.Rectangle // crop-local rows read, with context
	own   [2]int          // crop-local rows owned: words centred there are kept
	hash  uint64
	hit   bool
	gray  *image.Gray
	words []OCRWord
	err   error
}

func (w *worker) scan(ctx context.Context, req ports.HintScanRequest, fr frame, captureDur time.Duration, res ports.HintScanResult, atspi *pendingSource) ports.HintScanResult {
	log := logging.For(ctx, "hints")
	ar := atspi.wait()
	if ar.err != nil && !w.warnedSource {
		log.Warn().Err(ar.err).Msg("accessibility targets unavailable; using OCR only")
		w.warnedSource = true
	}
	out := make([]ports.HintTarget, 0, len(ar.targets))
	for _, t := range ar.targets {
		if !req.LinksOnly || t.Kind == ports.HintLink {
			out = append(out, t)
		}
	}
	nAtspi := len(out)
	t0 := time.Now()
	if w.cacheSeed == (maphash.Seed{}) {
		w.cacheSeed = maphash.MakeSeed()
	}
	nextCache := make(map[bandKey]bandCache)
	var crops []image.Rectangle
	var allWords []OCRWord
	pixels, links := 0, 0
	ocrOn := fr.pix != nil && w.ocrReady(ctx)
	// Phase 1 reads the frame: the bands that miss the cache are copied, as
	// grayscale, into one arena. Phase 2 (OCR) does not need the frame.
	var jobs []*bandJob
	var rects []image.Rectangle // crop of each window that has bands
	var arena *mapping
	if ocrOn {
		type pending struct {
			r    image.Rectangle
			jobs []*bandJob
		}
		var wins []pending
		need := 0
		for _, win := range req.Windows {
			if ar.covered[win.ID] {
				continue
			}
			r := windowCrop(win.Rect, req.Scale, req.Width, req.Height)
			if r.Empty() {
				continue
			}
			crops = append(crops, r)
			pixels += r.Dx() * r.Dy()
			p := pending{r: r}
			for _, b := range bandsOf(r.Dy()) {
				ext := image.Rect(0, b.top, r.Dx(), b.bottom)
				j := &bandJob{win: len(wins), ext: ext, own: [2]int{b.ownTop, b.ownBottom},
					key: bandKey{output: req.Output, window: win.ID, band: b.idx, bands: b.n}}
				j.hash = w.cropHash(fr.pix, fr.stride, ext.Add(r.Min))
				entry, ok := w.cache[j.key]
				if ok && entry.hash == j.hash && entry.ext == ext {
					j.hit, j.words = true, entry.words
				} else {
					need += ext.Dx() * ext.Dy()
				}
				p.jobs = append(p.jobs, j)
			}
			wins = append(wins, p)
		}
		if need > 0 {
			arena = w.alloc(need)
		}
		off := 0
		for _, p := range wins {
			rects = append(rects, p.r)
			for _, j := range p.jobs {
				if !j.hit {
					n := j.ext.Dx() * j.ext.Dy()
					j.gray = cropGray(fr.pix, fr.stride, j.ext.Add(p.r.Min), arena.b[off:off+n])
					off += n
				}
				jobs = append(jobs, j)
			}
		}
	} else if w.dumpDir != "" {
		for _, win := range req.Windows {
			if !ar.covered[win.ID] {
				if r := windowCrop(win.Rect, req.Scale, req.Width, req.Height); !r.Empty() {
					crops = append(crops, r)
				}
			}
		}
	}
	if w.dumpDir != "" && fr.pix != nil {
		// The dump reads the frame after the result is sent and releases it.
		w.pending = &dumpJob{when: time.Now(), req: req, pix: fr.pix, m: fr.m, stride: fr.stride, crops: crops, captureD: captureDur, atspiD: ar.dur}
	} else {
		fr.m.unmap() // the crops are extracted: give the frame back
	}
	fr.pix = nil

	hits := 0
	if len(jobs) > 0 {
		w.recognize(ctx, jobs)
		for _, j := range jobs {
			if j.hit {
				hits++
			}
		}
	}
	arena.unmap()

	// Merge the bands of each window, in order.
	k := 0
	for wi, r := range rects {
		var words []OCRWord
		for ; k < len(jobs) && jobs[k].win == wi; k++ {
			j := jobs[k]
			if j.err != nil {
				failureWarning.Do(func() { log.Warn().Err(j.err).Msg("OCR unavailable; install tesseract and tesseract-data-eng") })
				continue
			}
			nextCache[j.key] = bandCache{hash: j.hash, ext: j.ext, words: j.words}
			for _, word := range j.words {
				word.Rect = word.Rect.Add(r.Min)
				if word.Underlined {
					word.UnderL, word.UnderR = word.UnderL+r.Min.X, word.UnderR+r.Min.X
				}
				if !duplicateWord(words, word) {
					words = append(words, word)
				}
			}
		}
		allWords = append(allWords, words...)
		targets := textLinks(words, req.Scale)
		links += len(targets)
		out = append(out, targets...)
	}
	w.cache = nextCache
	ocrDur := time.Since(t0)
	res.Targets = out
	if p := w.pending; p != nil {
		p.targets, p.atspi, p.words, p.scanD = out, nAtspi, allWords, ocrDur
	}
	log.Info().Str("output", req.Output).
		Float64("atspi_ms", ms(ar.dur)).Int("atspi_windows", len(ar.covered)).Int("atspi_targets", nAtspi).
		Int("ocr_windows", len(rects)).Float64("ocr_ms", ms(ocrDur)).Int("ocr_px", pixels).
		Int("ocr_bands", len(jobs)).Int("band_hits", hits).
		Int("words", len(allWords)).Int("ocr_links", links).Bool("cache_hit", len(jobs) > 0 && hits == len(jobs)).
		Int("targets", len(out)).Msg("hint scan")
	return res
}

// duplicateWord reports whether words already holds word (the same text at
// the same place), as happens where two bands overlap.
func duplicateWord(words []OCRWord, word OCRWord) bool {
	for _, old := range words {
		if strings.EqualFold(word.Text, old.Text) && overlap(word.Rect, old.Rect) > .5 {
			return true
		}
	}
	return false
}

// bandKey identifies one OCR band of a window on an output.
type bandKey struct {
	output string
	window ports.WindowID
	band   int
	bands  int
}

// bandCache is the OCR result of a band whose pixels hash to hash. words are
// crop-local and only the ones the band owns.
type bandCache struct {
	hash  uint64
	ext   image.Rectangle
	words []OCRWord
}

// windowCrop rounds outwards in physical pixels, then intersects the output.
func windowCrop(r ports.Rect, scale float64, width, height int) image.Rectangle {
	if scale <= 0 {
		scale = 1
	}
	if r.W <= 0 || r.H <= 0 {
		return image.Rectangle{}
	}
	return image.Rect(int(math.Floor(float64(r.X)*scale)), int(math.Floor(float64(r.Y)*scale)),
		int(math.Ceil(float64(r.X+r.W)*scale)), int(math.Ceil(float64(r.Y+r.H)*scale))).Intersect(image.Rect(0, 0, width, height))
}

// cropHash hashes the raw pixels of r without copying them.
func (w *worker) cropHash(buf []byte, stride int, r image.Rectangle) uint64 {
	var h maphash.Hash
	h.SetSeed(w.cacheSeed)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		_, _ = h.Write(buf[y*stride+r.Min.X*4 : y*stride+r.Max.X*4])
	}
	return h.Sum64()
}

// cropGray converts the BGRA pixels of r to 8-bit luma into dst, which must
// hold r.Dx()*r.Dy() bytes; only this band is converted, not the output.
func cropGray(buf []byte, stride int, r image.Rectangle, dst []byte) *image.Gray {
	img := &image.Gray{Pix: dst[:r.Dx()*r.Dy()], Stride: r.Dx(), Rect: image.Rect(0, 0, r.Dx(), r.Dy())}
	for y := 0; y < r.Dy(); y++ {
		src := buf[(r.Min.Y+y)*stride+r.Min.X*4:][:r.Dx()*4]
		row := img.Pix[y*img.Stride:][:r.Dx()]
		for x := range row {
			p := src[x*4 : x*4+4 : x*4+4]
			row[x] = uint8((uint32(p[2])*299 + uint32(p[1])*587 + uint32(p[0])*114 + 500) / 1000)
		}
	}
	return img
}

// pendingSource is an accessibility query in flight.
type pendingSource struct {
	done chan struct{}
	res  atspiResult
}

// startSource starts the accessibility query of req, if there is a source and
// a window to ask about. done is closed when res is set.
func (w *worker) startSource(ctx context.Context, req ports.HintScanRequest) *pendingSource {
	p := &pendingSource{done: make(chan struct{})}
	if w.source == nil || len(req.Windows) == 0 {
		close(p.done)
		return p
	}
	go func() {
		defer close(p.done)
		t0 := time.Now()
		targets, covered, err := w.source.Targets(ctx, req.Windows, req.LinksOnly)
		p.res = atspiResult{targets: targets, covered: covered, dur: time.Since(t0), err: err}
	}()
	return p
}

// wait returns the result of the query.
func (p *pendingSource) wait() atspiResult {
	<-p.done
	return p.res
}
