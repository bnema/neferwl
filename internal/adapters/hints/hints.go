// Package hints finds clickable targets using ordered accessibility, OCR and
// detector sources, capturing pixels only when a listed source needs them.
package hints

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"slices"
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

type worker struct {
	sources []source
	warned  map[ports.HintSource]bool
	n       uint64
	dumpDir string
	pending *dumpJob
	// orphans are capture buffers the output may still write: they are
	// released when its CaptureDone finally arrives.
	orphans map[uint64]*mapping
	live    int // mappings not released yet
}

// frame is the captured output: BGRA pixels.
type frame struct {
	pix    []byte
	stride int
	m      *mapping
}

// String keeps mock diagnostics from reading pixels after the capture is unmapped.
func (f *frame) String() string {
	if f == nil {
		return "<nil frame>"
	}
	return fmt.Sprintf("frame(stride=%d)", f.stride)
}

func run(ctx context.Context, requests <-chan ports.HintScanRequest, captures chan<- ports.CaptureRequest, captured <-chan ports.CaptureDone, results chan<- ports.HintScanResult, timeout time.Duration, opts Options) error {
	w := &worker{dumpDir: opts.DumpDir, sources: makeSources(opts)}
	defer func() {
		for i := len(w.sources) - 1; i >= 0; i-- {
			_ = w.sources[i].Close()
		}
	}()
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

func (w *worker) serve(ctx context.Context, req ports.HintScanRequest, requests <-chan ports.HintScanRequest, captures chan<- ports.CaptureRequest, captured <-chan ports.CaptureDone, results chan<- ports.HintScanResult, timeout time.Duration) ports.HintScanResult {
	ctx, cancel := context.WithTimeout(ctx, scanBudget)
	defer cancel()
	res := ports.HintScanResult{Seq: req.Seq, Output: req.Output}
	if req.Width <= 0 || req.Height <= 0 || req.Width > maxPixels/req.Height {
		res.Err = fmt.Errorf("%w: %dx%d", ErrBadRequest, req.Width, req.Height)
		return res
	}
	var fr *frame
	var captureDur time.Duration
	defer func() {
		if fr != nil && (w.pending == nil || w.pending.m != fr.m) {
			fr.m.unmap()
		}
	}()
	capture := func() (*frame, error) {
		start := time.Now()
		f, err := w.capture(ctx, req, requests, captures, captured, results, timeout)
		captureDur = time.Since(start)
		fr = f
		return f, err
	}
	res.Targets, res.Err = w.chain(ctx, req, capture)
	if res.Err != nil {
		res.Targets = nil
		return res
	}
	if w.dumpDir != "" && fr != nil {
		w.pending = &dumpJob{when: time.Now(), req: req, pix: fr.pix, m: fr.m, stride: fr.stride, targets: res.Targets, captureD: captureDur}
	}
	log := logging.For(ctx, "hints")
	log.Info().Str("output", req.Output).Float64("capture_ms", ms(captureDur)).Int("targets", len(res.Targets)).Msg("hint scan")
	return res
}

const scanBudget = 6 * time.Second

func (w *worker) capture(ctx context.Context, req ports.HintScanRequest, requests <-chan ports.HintScanRequest, captures chan<- ports.CaptureRequest, captured <-chan ports.CaptureDone, results chan<- ports.HintScanResult, timeout time.Duration) (*frame, error) {
	// The capture buffer is anonymous mapped memory, outside the Go heap. It
	// is released once the detector answered, or by the dump job.
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
		return nil, ErrTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
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
				return nil, fmt.Errorf("hints: capture: %w", done.Err)
			}
			keep = true // caller owns the completed capture
			return &frame{pix: buf, stride: stride, m: m}, nil
		case other := <-requests:
			// The buffer is in use until the capture is done: answer now.
			busy := ports.HintScanResult{Seq: other.Seq, Output: other.Output, Err: ErrBusy}
			if !send(ctx, results, busy) {
				abandon()
				return nil, ctx.Err()
			}
		case <-timer.C:
			// The output may still write into buf: it is released when its
			// CaptureDone arrives.
			abandon()
			return nil, ErrTimeout
		case <-ctx.Done():
			abandon()
			return nil, ctx.Err()
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

// minTarget is the smallest target side kept, in logical pixels.
const minTarget = 4

// maxWorkBoxes bounds the quadratic cut-box and merge passes.
const maxWorkBoxes = 2000

// targetsOf converts boxes in physical pixels to logical output-local
// targets. A box with a non-finite coordinate or score, a score outside
// [0,1] or no area is dropped; the others are clamped to the width x height
// frame (physical), rounded outwards to logical pixels, and dropped when
// under minTarget on a side.
func targetsOf(dets []detection, scale float64, width, height int) []ports.HintTarget {
	if scale <= 0 {
		scale = 1
	}
	fw, fh := float64(width), float64(height)
	out := make([]ports.HintTarget, 0, len(dets))
	for _, d := range dets {
		v := [4]float64{float64(d.x1), float64(d.y1), float64(d.x2), float64(d.y2)}
		ok := d.score >= 0 && d.score <= 1 // false for NaN too
		for _, f := range v {
			ok = ok && !math.IsNaN(f) && !math.IsInf(f, 0)
		}
		if !ok || v[2] <= v[0] || v[3] <= v[1] {
			continue
		}
		// Clamp before any int conversion: huge finite values stay safe.
		x1, y1 := math.Max(0, math.Min(v[0], fw)), math.Max(0, math.Min(v[1], fh))
		x2, y2 := math.Max(0, math.Min(v[2], fw)), math.Max(0, math.Min(v[3], fh))
		lx1, ly1 := int(math.Floor(x1/scale)), int(math.Floor(y1/scale))
		lx2, ly2 := int(math.Ceil(x2/scale)), int(math.Ceil(y2/scale))
		if lx2-lx1 < minTarget || ly2-ly1 < minTarget {
			continue
		}
		out = append(out, ports.HintTarget{Rect: ports.Rect{X: lx1, Y: ly1, W: lx2 - lx1, H: ly2 - ly1}, Score: d.score})
	}
	return out
}

// prepareDetections validates and clamps boxes before any pairwise work,
// preserving their region metadata for cut-edge suppression.
func prepareDetections(in []detection, regions []region, width, height int) []detection {
	out := make([]detection, 0, len(in))
	for _, d := range in {
		if d.region < 0 || d.region >= max(1, len(regions)) || !(d.score >= 0 && d.score <= 1) {
			continue
		}
		valid := true
		for _, v := range []float32{d.x1, d.y1, d.x2, d.y2} {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		d.x1 = max(0, min(d.x1, float32(width)))
		d.x2 = max(0, min(d.x2, float32(width)))
		d.y1 = max(0, min(d.y1, float32(height)))
		d.y2 = max(0, min(d.y2, float32(height)))
		if d.x2 <= d.x1 || d.y2 <= d.y1 {
			continue
		}
		out = append(out, d)
	}
	slices.SortStableFunc(out, func(a, b detection) int { return cmp.Compare(b.score, a.score) })
	if len(out) > maxWorkBoxes {
		out = out[:maxWorkBoxes]
	}
	return out
}
