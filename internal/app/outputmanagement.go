package app

import (
	"errors"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

// errApplySuperseded answers a request whose configuration a reload replaced.
var errApplySuperseded = errors.New("output apply superseded by reload")

// outputApply owns output management from request to reply: runtime
// overrides, supersede by reload, and rollback after a failed backend apply.
// It belongs to the backend owner loop, which starts the configuration next
// returns, reports each result through finished and drains the outbox (core
// configuration, Wayland replies and inventory) with sends that never block
// the loop.
type outputApply struct {
	state *core.OutputOverrides
	log   zerowrap.Logger
	// running is set while the backend applies a configuration. The next
	// one waits in start until it finishes: configurations never overlap.
	running bool
	start   ports.Config
	pending bool
	// req is the request behind the backend work; nil for a reload or after
	// a reload superseded it.
	req      *ports.OutputApply
	previous map[string]ports.OutputConfig
	rollback bool
	failure  error
	// config is the latest effective configuration core has not received.
	config  ports.ConfigChanged
	publish bool
	replies []ports.OutputApplied
	// headsDue is set while Wayland has not received the latest inventory.
	headsDue bool
}

func newOutputApply(state *core.OutputOverrides, log zerowrap.Logger) *outputApply {
	return &outputApply{state: state, log: log}
}

// reload replaces the file configuration and supersedes the pending request.
// Only the latest reload needs backend work.
func (a *outputApply) reload(file ports.Config) {
	cfg := a.state.Reload(file)
	a.setConfig(cfg)
	if a.req != nil {
		a.reply(a.req.ID, errApplySuperseded)
		a.req = nil
	}
	if !a.state.Headless() {
		a.due(cfg)
	}
}

// heads records the backend inventory: it validates requests and waits for
// Wayland in the outbox, latest only.
func (a *outputApply) heads(heads ports.OutputHeads) {
	a.state.SetHeads(heads)
	a.headsDue = true
}

// request validates a protocol request. Test, invalid and headless requests
// are answered at once; others wait for the backend.
func (a *outputApply) request(req ports.OutputApply) {
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
func (a *outputApply) next() (ports.Config, bool) {
	if a.running || !a.pending {
		return ports.Config{}, false
	}
	cfg := a.start
	a.start, a.pending, a.running = ports.Config{}, false, true
	return cfg, true
}

// finished records the backend result of the running configuration. When a
// reload is waiting, the result belongs to a superseded configuration and is
// dropped. After a failed request the previous overrides return and the
// rollback configuration becomes due; the request is answered with the
// original failure once the rollback is done.
func (a *outputApply) finished(err error) {
	if !a.running {
		return
	}
	a.running = false
	if a.pending {
		return
	}
	req := a.req
	if req == nil {
		return
	}
	if a.rollback {
		a.req = nil
		a.reply(req.ID, a.failure)
		return
	}
	if err != nil {
		cfg := a.state.Restore(a.previous)
		a.setConfig(cfg)
		a.rollback, a.failure = true, err
		a.due(cfg)
		return
	}
	a.req = nil
	for _, h := range req.Heads {
		e := a.log.Info().Str("output", h.Name).Bool("enabled", h.Enabled).Float64("scale", h.Scale)
		if h.Transform != nil {
			e = e.Uint8("transform", uint8(*h.Transform))
		}
		e.Msg("output configuration applied")
	}
	a.reply(req.ID, nil)
}

// due replaces the configuration waiting to start: only the latest counts.
func (a *outputApply) due(cfg ports.Config) {
	a.start, a.pending = cfg, true
}

func (a *outputApply) setConfig(cfg ports.Config) {
	a.config = ports.ConfigChanged{Config: cfg}
	a.publish = true
}

func (a *outputApply) reply(id uint64, err error) {
	if err != nil {
		a.log.Warn().Err(err).Msg("output configuration failed")
	}
	a.replies = append(a.replies, ports.OutputApplied{ID: id, Err: err})
}

// configOut returns the core channel when a configuration waits, else nil so
// the owner loop's send case stays disabled.
func (a *outputApply) configOut(ch chan<- ports.ConfigChanged) chan<- ports.ConfigChanged {
	if !a.publish {
		return nil
	}
	return ch
}

// configSent records that core received the latest configuration.
func (a *outputApply) configSent() { a.publish = false }

// replyOut returns the reply channel and the oldest reply, or a nil channel.
func (a *outputApply) replyOut(ch chan<- ports.OutputApplied) (chan<- ports.OutputApplied, ports.OutputApplied) {
	if len(a.replies) == 0 {
		return nil, ports.OutputApplied{}
	}
	return ch, a.replies[0]
}

// headsOut returns the Wayland inventory channel and the latest inventory,
// or a nil channel when Wayland has it.
func (a *outputApply) headsOut(ch chan<- ports.OutputHeads) (chan<- ports.OutputHeads, ports.OutputHeads) {
	if !a.headsDue {
		return nil, ports.OutputHeads{}
	}
	return ch, a.state.Heads()
}

// headsSent records that Wayland received the latest inventory.
func (a *outputApply) headsSent() { a.headsDue = false }

// replySent drops the delivered reply.
func (a *outputApply) replySent() {
	a.replies[0] = ports.OutputApplied{}
	a.replies = a.replies[1:]
}
