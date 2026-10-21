// Package hints finds clickable targets for hint mode: it captures the last
// frame of an output through the capture pipeline and sends it to the
// nefer-hints detector, a persistent subprocess.
package hints

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
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
	det     detector
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

func run(ctx context.Context, requests <-chan ports.HintScanRequest, captures chan<- ports.CaptureRequest, captured <-chan ports.CaptureDone, results chan<- ports.HintScanResult, timeout time.Duration, opts Options) error {
	w := &worker{dumpDir: opts.DumpDir, det: opts.detector}
	if w.det == nil {
		w.det = newProcDetector(opts.Command, opts.ModelDir, opts.Threshold)
	}
	// The worker goroutine is the only one to touch the detector.
	defer func() { _ = w.det.Close() }()
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
			res = w.scan(ctx, req, frame{pix: buf, stride: stride, m: m}, time.Since(start), res)
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

// minTarget is the smallest target side kept, in logical pixels.
const minTarget = 4

// scan sends the frame to the detector and turns the boxes into targets. It
// releases the frame, or hands it to the dump job.
func (w *worker) scan(ctx context.Context, req ports.HintScanRequest, fr frame, captureDur time.Duration, res ports.HintScanResult) ports.HintScanResult {
	log := logging.For(ctx, "hints")
	t0 := time.Now()
	dets, err := w.det.Detect(ctx, detFrame{width: req.Width, height: req.Height, stride: fr.stride, m: fr.m})
	detectDur := time.Since(t0)
	if err != nil {
		fr.m.unmap()
		log.Info().Err(err).Str("output", req.Output).Float64("detect_ms", ms(detectDur)).Msg("hint scan failed")
		res.Err = err
		return res
	}
	// Merge after rounding to logical pixels, on the union of all boxes.
	res.Targets = mergeTargets(targetsOf(dets, req.Scale, req.Width, req.Height))
	if w.dumpDir != "" {
		// The dump reads the frame after the result is sent and releases it.
		w.pending = &dumpJob{when: time.Now(), req: req, pix: fr.pix, m: fr.m, stride: fr.stride, targets: res.Targets, dets: dets, captureD: captureDur, detectD: detectDur}
	} else {
		fr.m.unmap()
	}
	log.Info().Str("output", req.Output).Float64("capture_ms", ms(captureDur)).Float64("detect_ms", ms(detectDur)).
		Int("boxes", len(dets)).Int("targets", len(res.Targets)).Msg("hint scan")
	return res
}

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
