package wayland

import (
	"slices"
	"sync"
	"time"

	"github.com/bnema/neferwl/internal/adapters/outputkit/presented"
	"github.com/bnema/neferwl/internal/ports"
)

// maxPresentRing bounds the reports waiting for the display goroutine. Output
// queues are flushed every loop turn and hold no backlog, so the ring only
// fills when the display loop is stalled; this sink alone then merges flips.
const maxPresentRing = 64

// mergeWarnEvery spaces the warnings about merged flips.
const mergeWarnEvery = 10 * time.Second

// presentSink is the ports.PresentationSink wayland offers to the outputs:
// output owner goroutines Push reports, the display goroutine drains them and
// paces frames. The ring is a transport queue between those goroutines, not
// window state: the mutex guards only the slices, the pending flag and the
// merge counters.
type presentSink struct {
	s *Server

	mu          sync.Mutex
	ring, spare []ports.OutputPresented
	pending     bool // a drain is posted and has not taken the ring yet
	// merged counts flips merged since the last warning;
	// lastWarn is when that warning was logged.
	merged       int
	lastWarn     time.Time
	drainFn      func()
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
// serves every report pushed before the drain runs. When the ring is full the
// flip of the oldest report is carried or merged forward; merges are counted
// and warned about at most every mergeWarnEvery.
func (p *presentSink) Push(r ports.OutputPresented) {
	p.mu.Lock()
	warn, merged := p.enqueueLocked(r)
	post := !p.pending
	p.pending = true
	p.mu.Unlock()
	if warn {
		p.s.log.Warn().Int("merged", merged).Msg("outputs report faster than the display loop drains; flips merged")
	}
	if post {
		p.s.display.Post(p.drainFn)
	}
}

// enqueueLocked appends r, making room first when the ring is full. It
// reports whether to warn now and how many flips were merged since the last
// warning.
func (p *presentSink) enqueueLocked(r ports.OutputPresented) (warn bool, merged int) {
	if len(p.ring) >= maxPresentRing {
		if p.makeRoomLocked(&r) {
			p.merged++
		}
		if now := p.s.clock.Now(); p.merged > 0 && now.Sub(p.lastWarn) >= mergeWarnEvery {
			warn, merged = true, p.merged
			p.merged, p.lastWarn = 0, now
		}
	}
	p.ring = append(p.ring, r)
	return warn, merged
}

// makeRoomLocked frees a ring slot for incoming without losing a flip: the
// oldest report that a later report of the same output follows (in the ring,
// or incoming itself) is removed and its flip carried into that report. A
// flip-less successor takes the flip over; a successor with a flip merges it
// (presented.Merge: the older feedback is discarded), which is what the
// result counts.
//
// When no report has a successor, every ring entry and incoming belong to
// distinct outputs: only possible with more than maxPresentRing outputs.
// Nothing is dropped then; the ring grows by one, bounded by the number of
// outputs, because the next report of any of them finds a successor.
func (p *presentSink) makeRoomLocked(incoming *ports.OutputPresented) (merged bool) {
	for i := range p.ring {
		next := p.successorLocked(i, incoming)
		if next == nil {
			continue
		}
		switch old := p.ring[i].Flip; {
		case old == nil:
		case next.Flip == nil:
			next.Flip = old
		default:
			presented.Merge(old, next.Flip)
			merged = true
		}
		p.ring = slices.Delete(p.ring, i, i+1)
		return merged
	}
	return false
}

// successorLocked returns the next report of ring[i]'s output: a later ring
// entry, else incoming; nil when none follows.
func (p *presentSink) successorLocked(i int, incoming *ports.OutputPresented) *ports.OutputPresented {
	for j := i + 1; j < len(p.ring); j++ {
		if p.ring[j].Output == p.ring[i].Output {
			return &p.ring[j]
		}
	}
	if incoming.Output == p.ring[i].Output {
		return incoming
	}
	return nil
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
}
