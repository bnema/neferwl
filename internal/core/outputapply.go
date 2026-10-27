package core

import (
	"errors"

	"github.com/bnema/neferwl/internal/domain"
)

// ErrApplySuperseded answers a request whose configuration a reload replaced.
var ErrApplySuperseded = errors.New("output apply superseded by reload")

// OutputApply owns output management from request to reply: runtime
// overrides, supersede by reload, and rollback after a failed backend apply.
// It belongs to the backend owner loop, which starts the configuration next
// returns, reports each result through finished and drains the outbox (core
// configuration, Wayland replies and inventory) with sends that never block
// the loop.
type OutputApply struct {
	state *OutputOverrides

	// running is set while the backend applies a configuration. The next
	// one waits in start until it finishes: configurations never overlap.
	running bool
	start   domain.Config
	pending bool
	// req is the request behind the backend work; nil for a reload or after
	// a reload superseded it.
	req      *domain.OutputApply
	previous map[string]domain.OutputConfig
	rollback bool
	failure  error
	// config is the latest effective configuration core has not received.
	config  domain.ConfigChanged
	publish bool
	replies []domain.OutputApplied
	// headsDue is set while Wayland has not received the latest inventory.
	headsDue bool
}

func NewOutputApply(state *OutputOverrides) *OutputApply {
	return &OutputApply{state: state}
}

// reload replaces the file configuration and supersedes the pending request.
// Only the latest reload needs backend work.
func (a *OutputApply) Reload(file domain.Config) {
	cfg := a.state.Reload(file)
	a.setConfig(cfg)
	if a.req != nil {
		a.reply(a.req.ID, ErrApplySuperseded)
		a.req = nil
	}
	if !a.state.Headless() {
		a.due(cfg)
	}
}

// heads records the backend inventory: it validates requests and waits for
// Wayland in the outbox, latest only.
func (a *OutputApply) Heads(heads domain.OutputHeads) {
	a.state.SetHeads(heads)
	a.headsDue = true
}

// request validates a protocol request. Test, invalid and headless requests
// are answered at once; others wait for the backend.
func (a *OutputApply) Request(req domain.OutputApply) {
	if a.running || a.pending {
		a.reply(req.ID, errors.New("output apply already pending"))
		return
	}
	previous := a.state.Snapshot()
	cfg, err := a.state.Apply(req)
	if err != nil || req.Test {
		a.reply(req.ID, err)
		return
	}
	a.setConfig(cfg)
	if a.state.Headless() {
		a.reply(req.ID, nil)
		return
	}
	a.req, a.previous, a.rollback, a.failure = &req, previous, false, nil
	a.due(cfg)
}

// next returns the configuration the backend must start now, once. It
// returns nothing while the backend applies the previous one.
func (a *OutputApply) Next() (domain.Config, bool) {
	if a.running || !a.pending {
		return domain.Config{}, false
	}
	cfg := a.start
	a.start, a.pending, a.running = domain.Config{}, false, true
	return cfg, true
}

// OutputApplyDecision exposes successful heads to the caller for logging.
type OutputApplyDecision struct{ Applied []domain.HeadChange }

// Finished records the backend result of the running configuration. When a
// reload is waiting, the result belongs to a superseded configuration and is
// dropped. After a failed request the previous overrides return and the
// rollback configuration becomes due; the request is answered with the
// original failure once the rollback is done.
func (a *OutputApply) Finished(err error) (decision OutputApplyDecision) {
	if !a.running {
		return decision
	}
	a.running = false
	if a.pending {
		return decision
	}
	req := a.req
	if req == nil {
		return decision
	}
	if a.rollback {
		a.req = nil
		a.reply(req.ID, a.failure)
		return decision
	}
	if err != nil {
		cfg := a.state.Restore(a.previous)
		a.setConfig(cfg)
		a.rollback, a.failure = true, err
		a.due(cfg)
		return decision
	}
	a.req = nil
	decision.Applied = req.Heads
	a.reply(req.ID, nil)
	return decision
}

// due replaces the configuration waiting to start: only the latest counts.
func (a *OutputApply) due(cfg domain.Config) {
	a.start, a.pending = cfg, true
}

func (a *OutputApply) setConfig(cfg domain.Config) {
	a.config = domain.ConfigChanged{Config: cfg}
	a.publish = true
}

func (a *OutputApply) reply(id uint64, err error) {
	a.replies = append(a.replies, domain.OutputApplied{ID: id, Err: err})
}

// configOut returns the core channel when a configuration waits, else nil so
// the owner loop's send case stays disabled.
func (a *OutputApply) ConfigOut(ch chan<- domain.ConfigChanged) chan<- domain.ConfigChanged {
	if !a.publish {
		return nil
	}
	return ch
}

// configSent records that core received the latest configuration.
func (a *OutputApply) ConfigSent() { a.publish = false }

// replyOut returns the reply channel and the oldest reply, or a nil channel.
func (a *OutputApply) ReplyOut(ch chan<- domain.OutputApplied) (chan<- domain.OutputApplied, domain.OutputApplied) {
	if len(a.replies) == 0 {
		return nil, domain.OutputApplied{}
	}
	return ch, a.replies[0]
}

// headsOut returns the Wayland inventory channel and the latest inventory,
// or a nil channel when Wayland has it.
func (a *OutputApply) HeadsOut(ch chan<- domain.OutputHeads) (chan<- domain.OutputHeads, domain.OutputHeads) {
	if !a.headsDue {
		return nil, domain.OutputHeads{}
	}
	return ch, a.state.Heads()
}

// headsSent records that Wayland received the latest inventory.
func (a *OutputApply) HeadsSent() { a.headsDue = false }

// replySent drops the delivered reply.
func (a *OutputApply) ReplySent() {
	a.replies[0] = domain.OutputApplied{}
	a.replies = a.replies[1:]
}

func (a *OutputApply) Config() domain.ConfigChanged { return a.config }
