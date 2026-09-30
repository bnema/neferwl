package capture

import (
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
	clean     cleaner // scratch of CleanScene, owner only
	off       *offscreen
	worker    sync.WaitGroup
}

func NewPipeline(ctx context.Context, replies chan<- ports.CaptureDone) *Pipeline {
	ctx, cancel := context.WithCancel(ctx)
	p := &Pipeline{ctx: ctx, cancel: cancel, replies: replies, jobs: make(chan *batch, pipelineSlots), completed: make(chan *batch, pipelineSlots)}
	p.worker.Go(p.run)
	return p
}

// Completed wakes the owner even when the output is otherwise idle. Its opaque
// token is passed directly to Recycle; callers never access batch storage.
func (p *Pipeline) Completed() <-chan *batch { return p.completed }

// Recycle returns a completed GPU readback lease on the renderer's owner.
func (p *Pipeline) Recycle(b *batch, r ports.Renderer) {
	r.EndCapture(b.frame)
	*b = batch{}
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
		err = fmt.Errorf("capture pipeline full or stopped")
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
