package wayland

import (
	"sync"
	"time"

	"github.com/bnema/purego-libwayland/server"
)

// postRing carries values from producer goroutines to the display goroutine
// without allocating. It is a transport queue, not window state: the mutex
// guards only the slices and the pending flag. Values are applied in order of
// arrival and never dropped. A ring built with a merge function may fold a
// pushed value into the last queued one (and only that one, under the mutex),
// so nothing is ever reordered.
//
// It binds its drain once and posts it at most once per batch: Display.Post
// is unbounded (a Post per value would let a fast producer grow the queue),
// so one pending drain serves every value pushed before it runs.
type postRing[T any] struct {
	display *server.Display
	apply   func(T)
	drainFn func()
	// merge, when set, tries to fold v into the last queued value, which has
	// not been drained yet. It runs under mu and must not block or allocate.
	merge func(last *T, v T) bool
	// space is signalled (never blocking) after each drain and after a push
	// that left room, to wake blocked pushers one after the other.
	space chan struct{}
	// waiting is a test seam, nil in production: it is signalled (never
	// blocking) each time a Push is about to wait on a full ring.
	waiting chan<- struct{}

	mu          sync.Mutex
	ring, spare []T
	limit       int
	pending     bool // a drain is posted and has not taken the ring yet
	stats       ringStats
}

// ringStats is what a ring saw since the last takeStats. It lets a live log
// tell a slow display goroutine (long queue, long push block) from a slow
// producer.
type ringStats struct {
	maxLen       int           // largest queued length
	merged       int           // pushes folded into a queued value
	maxPushBlock time.Duration // longest wait of one Push on a full ring
}

// takeStats returns the counters and resets them.
func (r *postRing[T]) takeStats() ringStats {
	r.mu.Lock()
	st := r.stats
	st.maxLen = max(st.maxLen, len(r.ring))
	r.stats = ringStats{}
	r.mu.Unlock()
	return st
}

// newPostRing returns a ring of limit values whose values apply runs on the
// display goroutine, in order. limit must be at least 1.
func newPostRing[T any](d *server.Display, limit int, apply func(T)) *postRing[T] {
	return newMergingPostRing(d, limit, apply, nil)
}

// newMergingPostRing is newPostRing with a merge function (see postRing). A
// merged push needs no room: it succeeds on a full ring.
func newMergingPostRing[T any](d *server.Display, limit int, apply func(T), merge func(last *T, v T) bool) *postRing[T] {
	r := &postRing[T]{
		display: d,
		apply:   apply,
		space:   make(chan struct{}, 1),
		ring:    make([]T, 0, limit),
		spare:   make([]T, 0, limit),
		limit:   limit,
		merge:   merge,
	}
	r.drainFn = r.drain
	return r
}

// Push queues v for the display goroutine and wakes it. A full ring blocks
// the caller, which must not be the display goroutine, until a drain made
// room; it reports false, with v dropped, once the display has stopped.
func (r *postRing[T]) Push(v T) bool {
	var blocked time.Duration // total wait of this push on a full ring
	for {
		r.mu.Lock()
		// A non-empty ring always has its drain posted, so the last value is
		// still queued: merging into it needs neither room nor a new post.
		if n := len(r.ring); r.merge != nil && n > 0 && r.merge(&r.ring[n-1], v) {
			r.stats.merged++
			r.stats.maxPushBlock = max(r.stats.maxPushBlock, blocked)
			r.mu.Unlock()
			return true
		}
		if len(r.ring) < r.limit {
			r.ring = append(r.ring, v)
			r.stats.maxLen = max(r.stats.maxLen, len(r.ring))
			r.stats.maxPushBlock = max(r.stats.maxPushBlock, blocked)
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
		// Only the blocking path reads the clock: the fast path stays free.
		start := time.Now()
		if r.waiting != nil {
			select {
			case r.waiting <- struct{}{}:
			default:
			}
		}
		select {
		case <-r.space:
			blocked += time.Since(start)
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
