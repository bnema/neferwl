package core

import (
	"math"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// controlWait is an action or injection waiting for its completion level,
// Until predicate or shots.
type controlWait struct {
	shots      []domain.ShotRequest
	before     domain.State
	level      domain.WaitLevel
	until      *domain.Until
	deadline   time.Time
	baseline   map[string]uint64
	targetSeqs map[string]uint64
	reply      chan<- ports.ControlReply
}

// outputWait is a runtime output add or removal: first the backend's
// answer, then the output appearing or leaving.
type outputWait struct {
	before   domain.State
	deadline time.Time
	runtime  <-chan ports.OutputRuntimeResult
	remove   bool
	output   string
	reply    chan<- ports.ControlReply
}

// fillSeqs writes each named screen's last published scene sequence.
func (c *Core) fillSeqs(seqs map[string]uint64) map[string]uint64 {
	for _, sc := range c.screens {
		if sc.name() != "" {
			seqs[sc.name()] = sc.last.Seq
		}
	}
	return seqs
}

// sceneSeqs is a baseline the caller keeps.
func (c *Core) sceneSeqs() map[string]uint64 {
	return c.fillSeqs(make(map[string]uint64, len(c.screens)))
}

// scratchSeqs fills the reused scene sequence map for this pass.
func (c *Core) scratchSeqs() map[string]uint64 {
	if c.waitSeqs == nil {
		c.waitSeqs = make(map[string]uint64, len(c.screens))
	}
	clear(c.waitSeqs)
	return c.fillSeqs(c.waitSeqs)
}

// waitControl is shared by action execution and input injection. before and
// baseline must be captured before the operation changes owner state.
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
		timeout = domain.DefaultControlTimeout
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
	now        time.Time
	seqs       map[string]uint64
	earliest   time.Time
	scratch    bool // c.waitState holds this pass's snapshot
	state      domain.State
	hasState   bool
	pending    []domain.WindowID
	hasPending bool
}

// view is this pass's snapshot in the owner's scratch storage: for
// predicates only, never kept past the pass.
func (p *waitPass) view() domain.State {
	if !p.scratch {
		p.c.waitState = p.c.stateInto(p.c.waitState)
		p.scratch = true
	}
	return p.c.waitState
}

// State is this pass's snapshot for replies: a copy the client keeps.
func (p *waitPass) State() domain.State {
	if !p.hasState {
		p.state, p.hasState = cloneState(p.view()), true
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

func (p *waitPass) reply(before domain.State, ch chan<- ports.ControlReply, r ports.ControlReply) {
	r.State = p.State()
	r.Diff = domain.DiffState(before, r.State)
	controlReply(ch, r)
}

func (p *waitPass) keep(deadline time.Time) { p.earliest = earlier(p.earliest, deadline) }

// evalWaits runs at the top of every owner-loop pass, including passes reached
// via continue, after the preceding event and its publications have completed.
func (c *Core) evalWaits() {
	if len(c.controlWaits) == 0 && len(c.outputWaits) == 0 {
		c.stopControlWaitTimer()
		return
	}
	pass := waitPass{c: c, now: c.now(), seqs: c.scratchSeqs()}
	kept := c.outputWaits[:0]
	for _, w := range c.outputWaits {
		if !c.evalOutputWait(&pass, &w) {
			kept = append(kept, w)
			pass.keep(w.deadline)
		}
	}
	clear(c.outputWaits[len(kept):])
	c.outputWaits = kept
	keptScene := c.controlWaits[:0]
	for _, w := range c.controlWaits {
		if !c.evalSceneWait(&pass, &w) {
			keptScene = append(keptScene, w)
			pass.keep(w.deadline)
		}
	}
	clear(c.controlWaits[len(keptScene):])
	c.controlWaits = keptScene
	if pass.earliest.IsZero() {
		c.stopControlWaitTimer()
		return
	}
	if c.controlWaitTimer == nil {
		c.controlWaitTimer = c.opts.Clock.NewTimer(pass.earliest.Sub(pass.now))
	} else {
		c.controlWaitTimer.Reset(pass.earliest.Sub(pass.now))
	}
	c.controlWaitC = c.controlWaitTimer.C()
}

// evalOutputWait reports whether w was answered.
func (c *Core) evalOutputWait(pass *waitPass, w *outputWait) bool {
	if !pass.now.Before(w.deadline) {
		pass.reply(w.before, w.reply, ports.ControlReply{Err: ports.ControlErrTimeout, Done: domain.WaitCommitted})
		return true
	}
	if w.runtime != nil {
		select {
		case result := <-w.runtime:
			if result.Err != "" {
				controlReply(w.reply, ports.ControlReply{Err: result.Err})
				return true
			}
			w.output, w.runtime = result.Name, nil
		default:
			return false
		}
	}
	if _, exists := pass.seqs[w.output]; exists == w.remove {
		return false
	}
	pass.reply(w.before, w.reply, ports.ControlReply{Done: domain.WaitCommitted})
	return true
}

// evalSceneWait reports whether w was answered.
func (c *Core) evalSceneWait(pass *waitPass, w *controlWait) bool {
	seqs := pass.seqs
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
	timedOut := !pass.now.Before(w.deadline)
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
	satisfied := reached >= w.level && (w.until == nil || w.until.Holds(w.before, pass.view()))
	if !satisfied && !timedOut {
		return false
	}
	response := ports.ControlReply{Done: w.level}
	if !satisfied {
		response.Done = reached
		response.Err = ports.ControlErrTimeout
		response.Late = pass.Pending()
	}
	for _, shot := range w.shots {
		seq, exists := seqs[shot.Output]
		if exists && c.presented[shot.Output] >= seq {
			shot.MinSeq = seq
			shot.Width, shot.Height = c.shotSize(shot.Output)
			response.Shots = append(response.Shots, shot)
		}
	}
	pass.reply(w.before, w.reply, response)
	return true
}

// shotSize is the physical pixel size of name's presented scene: its logical
// size times its scale, in the buffer's orientation.
func (c *Core) shotSize(name string) (w, h int) {
	i := c.screenIndex(name)
	if i < 0 {
		return 0, 0
	}
	scene := c.screens[i].last
	scale := scene.Scale
	if scale <= 0 {
		scale = 1
	}
	w, h = int(math.Round(float64(scene.OutputWidth)*scale)), int(math.Round(float64(scene.OutputHeight)*scale))
	if scene.Transform.Rotated() {
		w, h = h, w
	}
	return w, h
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
