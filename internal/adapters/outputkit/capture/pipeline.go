package capture

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"bnema.dev/zerowrap"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/syncfile"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// MaxRequests bounds requests collected for one output frame.
const MaxRequests = 8

const pipelineSlots = 2

// ErrSecurityState rejects capture admission across an epoch or while protected.
var ErrSecurityState = errors.New("capture security epoch changed or session protected")

// batch remains owned by the worker until it is returned through completed.
// Both batches and their request storage are reused for the output's lifetime.
type batch struct {
	frame    ports.CaptureFrame
	requests [MaxRequests]domain.CaptureRequest
	count    int
	at       time.Time
	// gated: the worker delivers the requests only when the owner releases
	// the batch (see Pipeline.BeginGate). release carries its verdict; it is
	// made once and lives as long as the batch.
	gated   bool
	release chan error
	// copies are the GPU copies into the dmabuf destinations, by request
	// index: the done sync file (nil: finished) or the error. The owner
	// submits them (CopyTo) before handing the batch over, or for a gated
	// batch before releasing it; the worker waits, closes and answers.
	// Copies the worker never took are closed by Recycle.
	copies [MaxRequests]dmaCopy
}

// dmaCopy is the outcome of one CaptureFrame.CopyTo.
type dmaCopy struct {
	done *os.File
	err  error
}

// submitCopies starts the copy of each dmabuf request of b. Owner only.
func (b *batch) submitCopies() {
	if b.frame == nil {
		return
	}
	for i := range b.count {
		if q := b.requests[i]; q.DMABuf != nil {
			b.copies[i].done, b.copies[i].err = b.frame.CopyTo(q.Region, *q.DMABuf)
		}
	}
}

// closeCopies closes the sync files of b's copies.
func (b *batch) closeCopies(log zerowrap.Logger) {
	for i := range b.copies {
		if f := b.copies[i].done; f != nil {
			logging.CloseFile(log, f, "capture copy fence")
		}
		b.copies[i] = dmaCopy{}
	}
}

// Pipeline moves fence waiting and SHM copies off the renderer's owner.
// Submit, Recycle and Close belong to that owner; one worker handles all jobs.
type Pipeline struct {
	// Set before owner use; no mutation or worker access to this dependency.
	// Cursor is the output owner's immutable snapshot for this frame. Hidden
	// workspace/window child pipelines deliberately have no output cursor.
	Cursor   *domain.CaptureCursor
	Security ports.SessionSecurity
	// Log receives release failures of fences; set it before EnableOffscreen.
	// The zero Logger discards them.
	Log zerowrap.Logger
	// CPUReadback lets a plain sRGB capture that BeginCapture refuses (a
	// device without sync-file export) be read back with Renderer.Pixels,
	// which waits for the GPU on the owner. Headless only: it has no display
	// to pace, and already waits for its frames on the CPU.
	CPUReadback bool
	ctx         context.Context
	cancel      context.CancelFunc
	replies     chan<- domain.CaptureDone
	jobs        chan *batch
	completed   chan *batch
	batches     [pipelineSlots]batch
	// gating is set between BeginGate and EndGate; held are the batches
	// submitted meanwhile, waiting for their verdict. Owner only.
	gating        bool
	held          [pipelineSlots]*batch
	nheld         int
	excl          excluder         // scratch of ExcludedScene, owner only
	plainOverlays []domain.Overlay // scratch of SubmitPlain, owner only
	// split holds SubmitScoped's requests by readback (SDR, PQ), owner only.
	cursorSplit [2][MaxRequests]domain.CaptureRequest
	split       [2][MaxRequests]domain.CaptureRequest
	off         *offscreen
	worker      sync.WaitGroup
}

func NewPipeline(ctx context.Context, replies chan<- domain.CaptureDone) *Pipeline {
	ctx, cancel := context.WithCancel(ctx)
	p := &Pipeline{ctx: ctx, cancel: cancel, replies: replies, jobs: make(chan *batch, pipelineSlots), completed: make(chan *batch, pipelineSlots)}
	for i := range p.batches {
		p.batches[i].release = make(chan error, 1)
	}
	p.worker.Go(p.run)
	return p
}

// Completed wakes the owner even when the output is otherwise idle. Its opaque
// token is passed directly to Recycle; callers never access batch storage.
func (p *Pipeline) Completed() <-chan *batch { return p.completed }

// Recycle returns a completed GPU readback lease on the renderer's owner.
func (p *Pipeline) Recycle(b *batch, r ports.Renderer) {
	b.closeCopies(p.Log)
	if _, cpu := b.frame.(*pixelsFrame); !cpu {
		r.EndCapture(b.frame)
	}
	rel := b.release
	select { // a verdict nobody took (the worker was cancelled) must not leak to the next use
	case verdict := <-rel:
		_ = verdict // the gate verdict is obsolete: the worker was cancelled, nothing failed
	default:
	}
	*b = batch{release: rel}
}

// GateVerdict is what a batch held by the indicator gate is failed with when
// the frame that carries the indicator did not reach the display: err wrapped
// in ErrIndicatorMissing; nil stays nil (released).
func GateVerdict(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrIndicatorMissing, err)
}

// ErrFrameNotPresented fails a gated batch whose owner never gave a verdict:
// the frame it waited for was dropped on a path that did not present it.
var ErrFrameNotPresented = fmt.Errorf("%w: frame not presented", ErrIndicatorMissing)

// BeginGate makes the batches submitted from now on wait for EndGate: their
// worker does its fence wait, then holds the pixels back until the owner says
// the frame that carries the capture indicator is on screen. See EndGate.
func (p *Pipeline) BeginGate() { p.gating = true }

// EndGate gives the verdict on the batches held since BeginGate and stops
// gating. A nil err releases them: their captures are written and answered. A
// non-nil err fails every one of their requests with it: no pixel is
// delivered. It covers the child pipeline of a hidden workspace too. Nothing
// held, nothing done: no allocation, no cost while no capture is gated.
func (p *Pipeline) EndGate(err error) {
	if p == nil {
		return
	}
	p.gating = false
	for i := range p.nheld {
		b := p.held[i]
		p.held[i] = nil
		if len(b.release) > 0 {
			continue // the verdict is already there
		}
		if err == nil && p.ctx.Err() == nil {
			// Released: only now may pixels reach the client's buffers. A
			// cancelled pipeline fails the batch instead (process sees the
			// context), so no GPU copy is started for it.
			b.submitCopies()
		}
		// The worker only receives: the slot is free (owner only sends).
		b.release <- err
	}
	p.nheld = 0
	if p.off != nil && p.off.p != nil {
		p.off.p.EndGate(err)
	}
}

// SubmitScoped consumes requests with explicit immutable scene admission. The
// final gate read precedes BeginCapture, not the render that produced pixels.
//
// Screencast buffers in CastXBGR2101010 get the PQ words (BeginCapturePQ) in
// a batch of their own; a frame that is not HDR fails them transiently.
func (p *Pipeline) SubmitScoped(state domain.SecurityState, r ports.Renderer, requests []domain.CaptureRequest) {
	if p.Cursor == nil || len(requests) > MaxRequests {
		p.submitReadback(state, r, requests, nil)
		return
	}
	var n [2]int
	for _, q := range requests {
		k := 0
		if q.Cursor {
			k = 1
		}
		p.cursorSplit[k][n[k]] = q
		n[k]++
	}
	p.submitReadback(state, r, p.cursorSplit[0][:n[0]], nil)
	p.submitReadback(state, r, p.cursorSplit[1][:n[1]], p.Cursor)
	p.cursorSplit = [2][MaxRequests]domain.CaptureRequest{}
}

func (p *Pipeline) submitReadback(state domain.SecurityState, r ports.Renderer, requests []domain.CaptureRequest, cursor *domain.CaptureCursor) {
	npq := 0
	for _, q := range requests {
		if isPQ(q) {
			npq++
		}
	}
	if npq == 0 || len(requests) > MaxRequests {
		p.submitBatch(state, r, requests, false, cursor)
		return
	}
	if npq == len(requests) {
		p.submitBatch(state, r, requests, true, cursor)
		return
	}
	var n [2]int
	for _, q := range requests {
		k := 0
		if isPQ(q) {
			k = 1
		}
		p.split[k][n[k]] = q
		n[k]++
	}
	p.submitBatch(state, r, p.split[0][:n[0]], false, cursor)
	p.submitBatch(state, r, p.split[1][:n[1]], true, cursor)
	p.split = [2][MaxRequests]domain.CaptureRequest{}
}

// isPQ reports whether q wants the HDR frame's PQ words: a screencast
// buffer in XBGR2101010, copied on the GPU (client buffers are never PQ).
func isPQ(q domain.CaptureRequest) bool {
	return q.DMABuf != nil && q.DMABufBorrowed && q.DMABuf.Format == domain.CastXBGR2101010
}

// submitBatch is SubmitScoped for requests of one readback.
func (p *Pipeline) submitBatch(state domain.SecurityState, r ports.Renderer, requests []domain.CaptureRequest, pq bool, cursor *domain.CaptureCursor) {
	if len(requests) == 0 {
		return
	}
	// Reclaim returned leases before declaring the small ring saturated.
	p.reclaim(r)
	var b *batch
	if len(requests) <= MaxRequests && p.ctx.Err() == nil {
		for i := range p.batches {
			if p.batches[i].frame == nil {
				b = &p.batches[i]
				break
			}
		}
	}
	var err error
	if b == nil {
		err = fmt.Errorf("%w: capture pipeline full or stopped", ports.ErrCaptureTransient)
	} else if !p.captureAllowed(state) {
		err = ErrSecurityState
	} else {
		if cursor != nil {
			b.frame, err = r.BeginCaptureCursor(cursor, pq)
			if err != nil {
				err = fmt.Errorf("%w: cursor capture: %w", ports.ErrCaptureTransient, err)
			}
		} else if pq {
			if b.frame, err = r.BeginCapturePQ(); err != nil {
				// No HDR frame (the output just turned SDR) or no PQ pass
				// yet: the requester retries or renegotiates.
				err = fmt.Errorf("%w: PQ capture: %w", ports.ErrCaptureTransient, err)
			}
		} else {
			b.frame, err = r.BeginCapture()
			if err != nil && p.CPUReadback {
				// Pixels fails where BeginCapture failed for any other
				// reason (busy slots, no frame): the original error stands.
				if img := r.Pixels(); img != nil {
					b.frame, err = &pixelsFrame{img: img}, nil
				}
			}
		}
		if err == nil && b.frame == nil {
			err = fmt.Errorf("renderer returned no capture frame")
		}
	}
	if err != nil {
		for _, q := range requests {
			Fail(p.ctx, q, err, p.replies)
		}
		return
	}
	b.count = copy(b.requests[:], requests)
	b.at = monotonicNow()
	if b.gated = p.gating; b.gated {
		p.held[p.nheld] = b
		p.nheld++
	} else {
		b.submitCopies()
	}
	// There are exactly two reusable batches and two queue slots, so this
	// send cannot wait: no batch can be queued twice before recycling.
	p.jobs <- b
}

func (p *Pipeline) captureAllowed(state domain.SecurityState) bool {
	return p.Security == nil || !state.Protected && p.Security.Snapshot() == state
}

func (p *Pipeline) reclaim(r ports.Renderer) {
	for {
		select {
		case b := <-p.completed:
			p.Recycle(b, r)
		default:
			return
		}
	}
}

func (p *Pipeline) run() {
	for {
		select {
		case b := <-p.jobs:
			p.process(b)
		case <-p.ctx.Done():
			for {
				select {
				case b := <-p.jobs:
					p.process(b)
				default:
					return
				}
			}
		}
	}
}

func (p *Pipeline) process(b *batch) {
	err := p.ctx.Err()
	if err == nil && b.frame.Done() != nil {
		err = syncfile.Wait(p.ctx, b.frame.Done())
	}
	if b.gated {
		// The verdict is always taken, so none is left in the channel. The
		// owner's verdict, when it gave one, says why the capture failed
		// better than a cancelled context.
		var v error
		took := false
		select {
		case v = <-b.release:
			took = true
		case <-p.ctx.Done():
			select {
			case v = <-b.release:
				took = true
			default:
			}
		}
		if !took {
			// No verdict: the owner may still write the copies; it closes them.
			err = cmp.Or(err, p.ctx.Err())
			for i := range b.count {
				Fail(p.ctx, b.requests[i], err, p.replies)
			}
			p.completed <- b
			return
		}
		err = cmp.Or(v, err, p.ctx.Err())
	}
	for i := range b.count {
		q := b.requests[i]
		c := b.copies[i]
		b.copies[i] = dmaCopy{}
		switch {
		case err != nil:
			Fail(p.ctx, q, err, p.replies)
		case p.ctx.Err() != nil:
			Fail(p.ctx, q, p.ctx.Err(), p.replies)
		case q.DMABuf != nil:
			e := c.err
			if e == nil && c.done != nil {
				e = syncfile.Wait(p.ctx, c.done)
			}
			replyAt(p.ctx, q, e, b.at, p.replies)
		default:
			writeFrame(p.ctx, q, b.frame, b.at, p.replies)
		}
		if c.done != nil {
			logging.CloseFile(p.Log, c.done, "capture copy fence")
		}
	}
	p.completed <- b
}

// Close joins the worker before any renderer memory may be unmapped. A
// cancelled GPU operation is returned without reading its staging buffer;
// EndCapture must retain GPU-pending storage until completion.
func (p *Pipeline) Close(r ports.Renderer) {
	p.cancel()
	p.worker.Wait()
	p.reclaim(r)
	if p.off != nil {
		p.off.close()
	}
}
