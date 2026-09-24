package libinput

import (
	"context"
	"sync"
	"time"

	"github.com/bnema/nefertty/internal/ports"
)

// forwarder decouples reading libinput from core. libinput keeps reading
// devices at their full rate and never blocks on core; consecutive pointer
// motions still waiting to be sent collapse into the latest one, while keys
// and buttons keep their order relative to motion.
type forwarder struct {
	mu     sync.Mutex
	queue  []ports.InputEvent
	wake   chan struct{}
	stats  forwardStats
	notify func() // test hook: called after each send
}

// forwardStats is reset by take.
type forwardStats struct {
	Motions   int           // pointer motions read
	Coalesced int           // motions merged into a later one
	Sent      int           // events delivered to core
	MaxBlock  time.Duration // longest wait for core to accept one event
}

func newForwarder() *forwarder { return &forwarder{wake: make(chan struct{}, 1)} }

// push queues ev without blocking.
func (f *forwarder) push(ev ports.InputEvent) {
	f.mu.Lock()
	if m, ok := ev.(ports.PointerMotion); ok {
		f.stats.Motions++
		if n := len(f.queue); n > 0 {
			if _, tail := f.queue[n-1].(ports.PointerMotion); tail {
				f.queue[n-1] = m
				f.stats.Coalesced++
				f.mu.Unlock()
				return
			}
		}
	}
	f.queue = append(f.queue, ev)
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func (f *forwarder) pop() (ports.InputEvent, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queue) == 0 {
		return nil, false
	}
	ev := f.queue[0]
	f.queue[0] = nil
	f.queue = f.queue[1:]
	return ev, true
}

// take returns and resets the counters.
func (f *forwarder) take() forwardStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.stats
	f.stats = forwardStats{}
	return s
}

// run delivers queued events to input until ctx ends.
func (f *forwarder) run(ctx context.Context, input chan<- ports.InputEvent) {
	for {
		ev, ok := f.pop()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-f.wake:
				continue
			}
		}
		start := time.Now()
		select {
		case input <- ev:
		case <-ctx.Done():
			return
		}
		d := time.Since(start)
		f.mu.Lock()
		f.stats.Sent++
		f.stats.MaxBlock = max(f.stats.MaxBlock, d)
		f.mu.Unlock()
		if f.notify != nil {
			f.notify()
		}
	}
}
