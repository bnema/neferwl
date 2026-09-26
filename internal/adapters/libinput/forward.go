package libinput

import (
	"context"
	"sync"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

// forwarder decouples reading libinput from core. libinput keeps reading
// devices at their full rate and never blocks on core; consecutive pointer
// motions still waiting to be sent collapse into the latest one, while keys
// and buttons keep their order relative to motion.
type forwarder struct {
	mu    sync.Mutex
	queue []ports.InputEvent
	wake  chan struct{}
	stats forwardStats
	// The queue is unbounded (no event is ever dropped); long is set
	// while it is past forwardWarn entries, warned once each way.
	log  zerowrap.Logger
	long bool
}

// forwardWarn is the queue length that means core has stalled.
const forwardWarn = 4096

// forwardStats is reset by take.
type forwardStats struct {
	Motions   int           // pointer motions read
	Coalesced int           // motions merged into a later one
	Sent      int           // events delivered to core
	MaxBlock  time.Duration // longest wait for core to accept one event
}

func newForwarder(log zerowrap.Logger) *forwarder {
	return &forwarder{wake: make(chan struct{}, 1), log: log}
}

// push queues ev without blocking.
func (f *forwarder) push(ev ports.InputEvent) {
	f.mu.Lock()
	if m, ok := ev.(ports.PointerMotion); ok {
		f.stats.Motions++
		if n := len(f.queue); n > 0 {
			if prev, tail := f.queue[n-1].(ports.PointerMotion); tail {
				// Relative deltas add up: games read them, not the position.
				m.DX += prev.DX
				m.DY += prev.DY
				m.UnaccelDX += prev.UnaccelDX
				m.UnaccelDY += prev.UnaccelDY
				f.queue[n-1] = m
				f.stats.Coalesced++
				f.mu.Unlock()
				return
			}
		}
	}
	f.queue = append(f.queue, ev)
	if len(f.queue) > forwardWarn && !f.long {
		f.long = true
		f.log.Warn().Str("component", "input").Int("queued", len(f.queue)).Msg("core is not reading input; events queue up")
	}
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
	if f.long && len(f.queue) < forwardWarn {
		f.long = false
		f.log.Warn().Str("component", "input").Int("queued", len(f.queue)).Msg("input queue drained")
	}
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
	}
}
