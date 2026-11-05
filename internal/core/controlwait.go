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

// evalWaits runs at the top of every owner-loop pass, including passes reached
// via continue, after the preceding event and its publications have completed.
func (c *Core) evalWaits() {
	if len(c.controlWaits) == 0 {
		c.stopControlWaitTimer()
		return
	}
	now, state := c.now(), c.state()
	pending := c.pendingConfigures()
	settled := !c.animating() && len(pending) == 0
	seqs := c.sceneSeqs()
	kept := c.controlWaits[:0]
	var earliest time.Time
	for _, w := range c.controlWaits {
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
				kept = append(kept, w)
				continue
			}
		}
		if w.output != "" {
			exists := false
			for _, o := range state.Outputs {
				if o.Name == w.output {
					exists = true
				}
			}
			if exists == w.remove {
				kept = append(kept, w)
				continue
			}
			controlReply(w.reply, ports.ControlReply{Done: domain.WaitCommitted, State: state, Diff: domain.DiffState(w.before, state)})
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
		reached := domain.WaitCommitted
		if settled {
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
		satisfied := reached >= w.level && (w.until == nil || w.until.Holds(w.before, state))
		timedOut := !now.Before(w.deadline)
		if satisfied || timedOut {
			done := w.level
			if !satisfied {
				done = reached
			}
			response := ports.ControlReply{Done: done, State: state, Diff: domain.DiffState(w.before, state), TimedOut: !satisfied}
			if !satisfied {
				response.Err = "timeout"
				response.Late = pending
			}
			for _, shot := range w.shots {
				seq, exists := seqs[shot.Output]
				if exists && c.presented[shot.Output] >= seq {
					shot.MinSeq = seq
					response.Shots = append(response.Shots, shot)
				}
			}
			controlReply(w.reply, response)
			continue
		}
		kept = append(kept, w)
		if earliest.IsZero() || w.deadline.Before(earliest) {
			earliest = w.deadline
		}
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

func (c *Core) stopControlWaitTimer() {
	if c.controlWaitTimer != nil {
		c.controlWaitTimer.Stop()
	}
	c.controlWaitC = nil
}
