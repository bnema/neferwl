package wayland

import (
	"slices"
	"sync"
	"sync/atomic"

	"github.com/bnema/neferwl/internal/adapters/outputkit/presented"
	"github.com/bnema/neferwl/internal/ports"
)

// maxPresentRing bounds the reports waiting for the display goroutine. Each
// output already bounds its own queue (presented.MaxUnsent), so the ring only
// fills when the display loop is stalled.
const maxPresentRing = 64

// presentSink is the ports.PresentationSink wayland offers to the outputs:
// output owner goroutines Push reports, the display goroutine drains them and
// paces frames. The ring is a transport queue between those goroutines, not
// window state: the mutex guards only the two slices and the pending flag.
type presentSink struct {
	s *Server

	mu           sync.Mutex
	ring, spare  []ports.OutputPresented
	pending      bool // a drain is posted and has not taken the ring yet
	drainFn      func()
	drains       atomic.Uint64 // finished drains
	scratchFlips map[string]bool
}

func newPresentSink(s *Server) *presentSink {
	p := &presentSink{
		s:            s,
		ring:         make([]ports.OutputPresented, 0, maxPresentRing),
		spare:        make([]ports.OutputPresented, 0, maxPresentRing),
		scratchFlips: make(map[string]bool),
	}
	p.drainFn = p.drain
	return p
}

// Presentation returns the sink outputs report to. Push never blocks.
func (s *Server) Presentation() ports.PresentationSink { return s.sink }

// Push queues a report for the display goroutine and wakes it; one wake
// serves every report pushed before the drain runs.
func (p *presentSink) Push(r ports.OutputPresented) {
	p.mu.Lock()
	if len(p.ring) >= maxPresentRing {
		p.dropOldestLocked()
	}
	p.ring = append(p.ring, r)
	post := !p.pending
	p.pending = true
	p.mu.Unlock()
	if post {
		p.s.display.Post(p.drainFn)
	}
}

// dropOldestLocked makes room: the oldest report that a later report of the
// same output follows is folded into it (flips merge as in presented.Queue).
func (p *presentSink) dropOldestLocked() {
	victim := 0
	for i := range p.ring {
		if next := p.nextOfOutputLocked(i); next > 0 {
			if old, nf := p.ring[i].Flip, p.ring[next].Flip; old != nil && nf != nil {
				presented.Merge(old, nf)
			}
			victim = i
			break
		}
	}
	p.ring = slices.Delete(p.ring, victim, victim+1)
}

// nextOfOutputLocked returns the index of the next report of i's output, 0
// when none follows.
func (p *presentSink) nextOfOutputLocked(i int) int {
	for j := i + 1; j < len(p.ring); j++ {
		if p.ring[j].Output == p.ring[i].Output {
			return j
		}
	}
	return 0
}

// drain runs on the display goroutine: it paces frames for every report that
// arrived since the last drain as one tick, so a burst of flips fires one
// burst of frame callbacks per output.
func (p *presentSink) drain() {
	p.mu.Lock()
	batch := p.ring
	p.ring, p.spare = p.spare[:0], nil
	p.pending = false
	p.mu.Unlock()
	flipped := p.scratchFlips
	clear(flipped)
	for _, r := range batch {
		flipped[r.Output] = flipped[r.Output] || paces(r)
	}
	if len(batch) > 0 {
		p.s.paceTick(flipped, batch)
	}
	// The reports carry maps: do not retain them in the spare batch.
	clear(batch)
	p.mu.Lock()
	p.spare = batch[:0]
	p.mu.Unlock()
	p.drains.Add(1)
}
