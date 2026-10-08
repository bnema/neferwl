package capture

import (
	"context"
	"errors"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// Reasons an output owner fails a request with.
var (
	ErrOutputStopped    = errors.New("output stopped")
	ErrOutputOff        = errors.New("output off")
	ErrSessionProtected = errors.New("session protected")
	ErrBatchFull        = errors.New("output capture batch full")
	ErrEpochChanged     = errors.New("security epoch changed")
)

// Requests are the capture requests an output owner collected for its next
// frame, in fixed storage (MaxRequests): no allocation. Its owner goroutine
// is the only user. List is the live slice the pipeline calls take and
// return; assign their result back to it.
type Requests struct {
	storage [MaxRequests]ports.CaptureRequest
	List    []ports.CaptureRequest
	ctx     context.Context
	replies chan<- ports.CaptureDone
}

// Init binds the requests to the owner's context and reply channel. It must
// run before any other method.
func (r *Requests) Init(ctx context.Context, replies chan<- ports.CaptureDone) {
	r.List, r.ctx, r.replies = r.storage[:0], ctx, replies
}

// Len is the number of requests held.
func (r *Requests) Len() int { return len(r.List) }

// Admit takes q for the next frame, or fails it with refuse when that is not
// nil, or when the batch is full. It reports whether q needs a new frame: its
// indicator is already on the current scene.
func (r *Requests) Admit(q ports.CaptureRequest, refuse error, scene ports.Scene, haveScene bool) (redraw bool) {
	if refuse == nil && len(r.List) == cap(r.List) {
		refuse = ErrBatchFull
	}
	if refuse != nil {
		Fail(r.ctx, q, refuse, r.replies)
		return false
	}
	q.Since = time.Now()
	r.List = append(r.List, q)
	// Its indicator may already be on screen; else it waits for the scene
	// that shows it.
	return haveScene && IndicatorShown(scene, q)
}

// FailPending fails every request not yet handed to the capture worker and
// empties the list.
func (r *Requests) FailPending(err error) {
	for _, q := range r.List {
		if !Handed(q) {
			Fail(r.ctx, q, err, r.replies)
		}
	}
	r.Reset()
}

// Reset empties the list without answering: its requests were answered or
// handed over.
func (r *Requests) Reset() {
	clear(r.List)
	r.List = r.storage[:0]
}

// Stop fails what the owner still holds, then what is queued on incoming,
// with ErrOutputStopped. It returns once incoming is empty or closed.
func (r *Requests) Stop(incoming <-chan ports.CaptureRequest) {
	for _, q := range r.List {
		if !Handed(q) {
			Fail(r.ctx, q, ErrOutputStopped, r.replies)
		}
	}
	for {
		select {
		case q, ok := <-incoming:
			if !ok {
				return
			}
			Fail(r.ctx, q, ErrOutputStopped, r.replies)
		default:
			return
		}
	}
}
