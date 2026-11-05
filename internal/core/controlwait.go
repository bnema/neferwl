package core

import (
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

type controlWait struct {
	shots      []domain.ShotRequest
	before     domain.State
	level      domain.WaitLevel
	until      *domain.Until
	deadline   time.Time
	baseline   map[string]uint64
	targetSeqs map[string]uint64
	runtime    <-chan ports.OutputRuntimeResult
	remove     bool
	output     string
	reply      chan<- ports.ControlReply
}

func (c *Core) sceneSeqs() map[string]uint64 {
	seqs := make(map[string]uint64, len(c.screens))
	for _, sc := range c.screens {
		if sc.name() != "" {
			seqs[sc.name()] = sc.last.Seq
		}
	}
	return seqs
}

// waitControl is shared by action execution and future input injection. before
// and baseline must be captured before the operation changes owner state.
func (c *Core) waitControl(before domain.State, baseline map[string]uint64, level domain.WaitLevel, until *domain.Until, timeout time.Duration, reply chan<- ports.ControlReply, shots []domain.ShotRequest) {
	if len(shots) > 0 {
		level = domain.WaitPresented
	}
	if level == domain.WaitCommitted && until == nil {
		now := c.state()
		controlReply(reply, ports.ControlReply{Done: domain.WaitCommitted, State: now, Diff: domain.DiffState(before, now)})
		return
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	var predicate *domain.Until
	if until != nil {
		copy := *until
		predicate = &copy
	}
	c.controlWaits = append(c.controlWaits, controlWait{shots: shots, before: before, baseline: baseline, targetSeqs: make(map[string]uint64), level: level, until: predicate, deadline: c.now().Add(timeout), reply: reply})
}

// waitPass caches what evalWaits derives from owner state: each value is
// computed on first use in a pass, so a wait that needs none of them costs
// nothing beyond the scratch scene sequences.
type waitPass struct {
	c          *Core
	state      domain.State
	hasState   bool
	pending    []domain.WindowID
	hasPending bool
}

func (p *waitPass) State() domain.State {
	if !p.hasState {
		p.state, p.hasState = p.c.state(), true
	}
	return p.state
}

func (p *waitPass) Pending() []domain.WindowID {
	if !p.hasPending {
		p.pending, p.hasPending = p.c.pendingConfigures(), true
	}
	return p.pending
}

func (p *waitPass) settled() bool {
	return !p.c.animating() && len(p.c.configures.pending) == 0
}

func (p *waitPass) reply(w controlWait, r ports.ControlReply) {
	r.State = p.State()
	r.Diff = domain.DiffState(w.before, r.State)
	controlReply(w.reply, r)
}

// scratchSeqs fills the reused scene sequence map for this pass.
func (c *Core) scratchSeqs() map[string]uint64 {
	if c.waitSeqs == nil {
		c.waitSeqs = make(map[string]uint64, len(c.screens))
	}
	clear(c.waitSeqs)
	for _, sc := range c.screens {
		if sc.name() != "" {
			c.waitSeqs[sc.name()] = sc.last.Seq
		}
	}
	return c.waitSeqs
}

// evalWaits runs at the top of every owner-loop pass, including passes reached
// via continue, after the preceding event and its publications have completed.
func (c *Core) evalWaits() {
	if len(c.controlWaits) == 0 {
		c.stopControlWaitTimer()
		return
	}
	now := c.now()
	pass := waitPass{c: c}
	seqs := c.scratchSeqs()
	kept := c.controlWaits[:0]
	var earliest time.Time
	for _, w := range c.controlWaits {
		if w.runtime != nil || w.output != "" {
			if !now.Before(w.deadline) {
				pass.reply(w, ports.ControlReply{Err: "timeout", TimedOut: true, Done: domain.WaitCommitted})
				continue
			}
			if w.runtime != nil {
				select {
				case result := <-w.runtime:
					if result.Err != "" {
						controlReply(w.reply, ports.ControlReply{Err: result.Err})
						continue
					}
					w.output = result.Name
					w.runtime = nil
				default:
				}
			}
			if w.runtime != nil {
				kept, earliest = append(kept, w), earlier(earliest, w.deadline)
				continue
			}
			_, exists := seqs[w.output]
			if exists == w.remove {
				kept, earliest = append(kept, w), earlier(earliest, w.deadline)
				continue
			}
			pass.reply(w, ports.ControlReply{Done: domain.WaitCommitted})
			continue
		}

		// Track final animation publications and scenes produced by later effects
		// (for example a map satisfying Until), not only the first action frame.
		for output, seq := range seqs {
			if seq != w.baseline[output] {
				w.targetSeqs[output] = seq
			}
		}
		// Unchanged outputs still require their published scene for a shot.
		for _, shot := range w.shots {
			if seq, exists := seqs[shot.Output]; exists {
				w.targetSeqs[shot.Output] = seq
			}
		}
		timedOut := !now.Before(w.deadline)
		reached := domain.WaitCommitted
		// The reached level matters only to a higher wait or to a timeout reply.
		if (w.level > domain.WaitCommitted || timedOut) && pass.settled() {
			reached = domain.WaitSettled
			presented := true
			for output, seq := range w.targetSeqs {
				if _, exists := seqs[output]; exists && c.presented[output] < seq {
					presented = false
					break
				}
			}
			if presented {
				reached = domain.WaitPresented
			}
		}
		satisfied := reached >= w.level && (w.until == nil || w.until.Holds(w.before, pass.State()))
		if satisfied || timedOut {
			done := w.level
			response := ports.ControlReply{Done: done, TimedOut: !satisfied}
			if !satisfied {
				response.Done = reached
				response.Err = "timeout"
				response.Late = pass.Pending()
			}
			for _, shot := range w.shots {
				seq, exists := seqs[shot.Output]
				if exists && c.presented[shot.Output] >= seq {
					shot.MinSeq = seq
					response.Shots = append(response.Shots, shot)
				}
			}
			pass.reply(w, response)
			continue
		}
		kept, earliest = append(kept, w), earlier(earliest, w.deadline)
	}
	clear(c.controlWaits[len(kept):])
	c.controlWaits = kept
	if earliest.IsZero() {
		c.stopControlWaitTimer()
		return
	}
	if c.controlWaitTimer == nil {
		c.controlWaitTimer = c.opts.Clock.NewTimer(earliest.Sub(now))
	} else {
		c.controlWaitTimer.Reset(earliest.Sub(now))
	}
	c.controlWaitC = c.controlWaitTimer.C()
}

// earlier returns the earlier of a running minimum (zero: none) and t.
func earlier(min, t time.Time) time.Time {
	if min.IsZero() || t.Before(min) {
		return t
	}
	return min
}

func (c *Core) stopControlWaitTimer() {
	if c.controlWaitC == nil {
		return
	}
	c.controlWaitTimer.Stop()
	c.controlWaitC = nil
}
