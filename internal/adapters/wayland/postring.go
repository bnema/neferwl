package wayland

import (
	"sync"

	"github.com/bnema/purego-libwayland/server"
)

// postRing carries values from producer goroutines to the display goroutine
// without allocating. It is a transport queue, not window state: the mutex
// guards only the slices and the pending flag. Values are never merged or
// dropped, in order of arrival.
//
// It binds its drain once and posts it at most once per batch: Display.Post
// is unbounded (a Post per value would let a fast producer grow the queue),
// so one pending drain serves every value pushed before it runs.
type postRing[T any] struct {
	display *server.Display
	apply   func(T)
	drainFn func()
	// space is signalled (never blocking) after each drain and after a push
	// that left room, to wake blocked pushers one after the other.
	space chan struct{}

	mu          sync.Mutex
	ring, spare []T
	limit       int
	pending     bool // a drain is posted and has not taken the ring yet
}

// newPostRing returns a ring of limit values whose values apply runs on the
// display goroutine, in order. limit must be at least 1.
func newPostRing[T any](d *server.Display, limit int, apply func(T)) *postRing[T] {
	r := &postRing[T]{
		display: d,
		apply:   apply,
		space:   make(chan struct{}, 1),
		ring:    make([]T, 0, limit),
		spare:   make([]T, 0, limit),
		limit:   limit,
	}
	r.drainFn = r.drain
	return r
}

// Push queues v for the display goroutine and wakes it. A full ring blocks
// the caller, which must not be the display goroutine, until a drain made
// room; it reports false, with v dropped, once the display has stopped.
func (r *postRing[T]) Push(v T) bool {
	for {
		r.mu.Lock()
		if len(r.ring) < r.limit {
			r.ring = append(r.ring, v)
			post := !r.pending
			r.pending = true
			room := len(r.ring) < r.limit
			r.mu.Unlock()
			if post {
				r.display.Post(r.drainFn)
			}
			if room {
				r.signal() // chain the wake to another blocked pusher
			}
			return true
		}
		r.mu.Unlock()
		select {
		case <-r.space:
		case <-r.display.Stopped():
			return false
		}
	}
}

func (r *postRing[T]) signal() {
	select {
	case r.space <- struct{}{}:
	default:
	}
}

// drain runs on the display goroutine and applies the batch in order.
func (r *postRing[T]) drain() {
	r.mu.Lock()
	batch := r.ring
	r.ring, r.spare = r.spare[:0], nil
	r.pending = false
	r.mu.Unlock()
	r.signal()
	for _, v := range batch {
		r.apply(v)
	}
	// Do not retain what the values reference in the spare batch.
	clear(batch)
	r.mu.Lock()
	r.spare = batch[:0]
	r.mu.Unlock()
}
