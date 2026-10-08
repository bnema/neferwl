package capture

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bnema/neferwl/internal/adapters/syncfile"
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
	requests [MaxRequests]ports.CaptureRequest
	count    int
	at       time.Time
	// gated: the worker delivers the requests only when the owner releases
	// the batch (see Pipeline.BeginGate). release carries its verdict; it is
	// made once and lives as long as the batch.
	gated   bool
	release chan error
}

// Pipeline moves fence waiting and SHM copies off the renderer's owner.
// Submit, Recycle and Close belong to that owner; one worker handles all jobs.
type Pipeline struct {
	// Set before owner use; no mutation or worker access to this dependency.
	Security  ports.SessionSecurity
	ctx       context.Context
	cancel    context.CancelFunc
	replies   chan<- ports.CaptureDone
	jobs      chan *batch
	completed chan *batch
	batches   [pipelineSlots]batch
	// gating is set between BeginGate and EndGate; held are the batches
	// submitted meanwhile, waiting for their verdict. Owner only.
	gating bool
	held   [pipelineSlots]*batch
	nheld  int
	excl   excluder // scratch of ExcludedScene, owner only
	off    *offscreen
	worker sync.WaitGroup
}

func NewPipeline(ctx context.Context, replies chan<- ports.CaptureDone) *Pipeline {
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
	r.EndCapture(b.frame)
	rel := b.release
	select { // a verdict nobody took (the worker was cancelled) must not leak to the next use
	case <-rel:
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
		select {
		case b.release <- err:
		default: // the verdict is already there
		}
	}
	p.nheld = 0
	if p.off != nil && p.off.p != nil {
		p.off.p.EndGate(err)
	}
}

// SubmitScoped consumes requests with explicit immutable scene admission. The
// final gate read precedes BeginCapture, not the render that produced pixels.
func (p *Pipeline) SubmitScoped(state ports.SecurityState, r ports.Renderer, requests []ports.CaptureRequest) {
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
		b.frame, err = r.BeginCapture()
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
	}
	// There are exactly two reusable batches and two queue slots, so this
	// send cannot wait: no batch can be queued twice before recycling.
	p.jobs <- b
}

func (p *Pipeline) captureAllowed(state ports.SecurityState) bool {
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
		select {
		case v = <-b.release:
		case <-p.ctx.Done():
			select {
			case v = <-b.release:
			default:
			}
		}
		err = cmp.Or(v, err, p.ctx.Err())
	}
	for i := range b.count {
		q := b.requests[i]
		if err != nil {
			Fail(p.ctx, q, err, p.replies)
		} else if e := p.ctx.Err(); e != nil {
			Fail(p.ctx, q, e, p.replies)
		} else {
			writeFrame(p.ctx, q, b.frame, b.at, p.replies)
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
