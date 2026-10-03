package app

import (
	"errors"
	"fmt"
	"image"
	"maps"
	"math"
	"reflect"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

// outputOverrides holds runtime-only output settings. Only outputApply uses
// it; the file configuration is never mutated.
type outputOverrides struct {
	file      ports.Config
	overrides map[string]ports.OutputConfig
	heads     ports.OutputHeads
	headless  bool
}

func newOutputOverrides(file ports.Config, headless bool) *outputOverrides {
	return &outputOverrides{file: file, headless: headless, overrides: map[string]ports.OutputConfig{}}
}
func (o *outputOverrides) effective() ports.Config {
	cfg := o.file
	cfg.Outputs = slices.Clone(o.file.Outputs)
	// Preserve file order and backend connection order; map iteration would
	// make automatic placement change unpredictably.
	for _, head := range o.heads.Heads {
		entry, ok := o.overrides[head.Info.Name]
		if !ok {
			continue
		}
		index := slices.IndexFunc(cfg.Outputs, func(v ports.OutputConfig) bool { return v.Name == head.Info.Name })
		if index < 0 {
			cfg.Outputs = append(cfg.Outputs, entry)
		} else {
			cfg.Outputs[index] = entry
		}
	}
	return cfg
}

// reload replaces the file configuration. Runtime overrides are dropped,
// unless only output scales changed (a saved scale bind): then they stay and
// take the new file scales.
func (o *outputOverrides) reload(file ports.Config) ports.Config {
	scales, onlyScales := scaleChanges(o.file, file)
	o.file = file
	if !onlyScales {
		clear(o.overrides)
		return o.effective()
	}
	for name, scale := range scales {
		if entry, ok := o.overrides[name]; ok {
			entry.Scale = scale
			o.overrides[name] = entry
		}
	}
	return o.effective()
}

// scaleChanges lists the output scales that differ from old to cur, and
// reports whether nothing else differs.
func scaleChanges(old, cur ports.Config) (map[string]float64, bool) {
	scaleOf := func(c ports.Config, name string) float64 {
		for _, o := range c.Outputs {
			if o.Name == name {
				return o.Scale
			}
		}
		return 0
	}
	// Without scales, an output.<name>.scale-only entry is empty: drop it.
	strip := func(c ports.Config) ports.Config {
		c.Outputs = slices.Clone(c.Outputs)
		for i := range c.Outputs {
			c.Outputs[i].Scale = 0
		}
		c.Outputs = slices.DeleteFunc(c.Outputs, func(o ports.OutputConfig) bool {
			return o == ports.OutputConfig{Name: o.Name, ScaleOnly: true, SDRBrightness: ports.DefaultSDRBrightness}
		})
		return c
	}
	if !reflect.DeepEqual(strip(old), strip(cur)) {
		return nil, false
	}
	scales := map[string]float64{}
	for _, o := range cur.Outputs {
		if o.Scale != scaleOf(old, o.Name) {
			scales[o.Name] = o.Scale
		}
	}
	return scales, true
}
func (o *outputOverrides) validate(req ports.OutputApply) error {
	if len(req.Heads) != len(o.heads.Heads) {
		return fmt.Errorf("incomplete head configuration")
	}
	seen := map[string]bool{}
	for _, change := range req.Heads {
		if seen[change.Name] {
			return fmt.Errorf("duplicate head %s", change.Name)
		}
		seen[change.Name] = true
		index := slices.IndexFunc(o.heads.Heads, func(h ports.OutputHead) bool { return h.Info.Name == change.Name })
		if index < 0 {
			return fmt.Errorf("unknown head %s", change.Name)
		}
		current := o.heads.Heads[index]
		if change.Scale < 0 || change.Scale > 4 || (change.Scale > 0 && change.Scale < 1) || math.IsNaN(change.Scale) || math.IsInf(change.Scale, 0) {
			return fmt.Errorf("invalid scale on %s", change.Name)
		}
		if change.Transform != nil && *change.Transform > 7 {
			return fmt.Errorf("invalid transform on %s", change.Name)
		}
		if o.headless && (change.Enabled != current.Enabled || change.Mode != nil && (current.Current == nil || change.Mode.Width != current.Current.Width || change.Mode.Height != current.Current.Height || change.Mode.RefreshMilli != current.Current.RefreshMilli)) {
			return fmt.Errorf("headless mode and enabled state are fixed")
		}
		if change.Mode != nil && !slices.ContainsFunc(current.Modes, func(m ports.OutputMode) bool {
			return m.Width == change.Mode.Width && m.Height == change.Mode.Height && m.RefreshMilli == change.Mode.RefreshMilli
		}) {
			return fmt.Errorf("unknown mode on %s", change.Name)
		}
	}
	return nil
}
func (o *outputOverrides) apply(req ports.OutputApply) (ports.Config, error) {
	if err := o.validate(req); err != nil {
		return ports.Config{}, err
	}
	if req.Test {
		return o.effective(), nil
	}
	for _, h := range req.Heads {
		entry, ok := o.overrides[h.Name]
		if !ok {
			index := slices.IndexFunc(o.file.Outputs, func(v ports.OutputConfig) bool { return v.Name == h.Name })
			if index >= 0 {
				entry = o.file.Outputs[index]
			} else {
				entry.Name = h.Name
			}
		}
		entry.Off = !h.Enabled
		if h.Scale > 0 {
			entry.Scale = h.Scale
		}
		if h.Transform != nil {
			entry.Transform = *h.Transform
		}
		if h.Pos != nil {
			entry.Pos = &image.Point{X: h.Pos.X, Y: h.Pos.Y}
		}
		if h.Mode != nil {
			entry.Mode = fmt.Sprintf("%dx%d@%.3f", h.Mode.Width, h.Mode.Height, float64(h.Mode.RefreshMilli)/1000)
		}
		o.overrides[h.Name] = entry
	}
	return o.effective(), nil
}

// errApplySuperseded answers a request whose configuration a reload replaced.
var errApplySuperseded = errors.New("output apply superseded by reload")

// outputApply owns output management from request to reply: runtime
// overrides, supersede by reload, and rollback after a failed backend apply.
// It belongs to the backend owner loop, which starts the configuration next
// returns, reports each result through finished and drains the outbox (core
// configuration, Wayland replies and inventory) with sends that never block
// the loop.
type outputApply struct {
	state *outputOverrides
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

func newOutputApply(state *outputOverrides, log zerowrap.Logger) *outputApply {
	return &outputApply{state: state, log: log}
}

// reload replaces the file configuration and supersedes the pending request.
// Only the latest reload needs backend work.
func (a *outputApply) reload(file ports.Config) {
	cfg := a.state.reload(file)
	a.setConfig(cfg)
	if a.req != nil {
		a.reply(a.req.ID, errApplySuperseded)
		a.req = nil
	}
	if !a.state.headless {
		a.due(cfg)
	}
}

// heads records the backend inventory: it validates requests and waits for
// Wayland in the outbox, latest only.
func (a *outputApply) heads(heads ports.OutputHeads) {
	a.state.heads = heads
	a.headsDue = true
}

// request validates a protocol request. Test, invalid and headless requests
// are answered at once; others wait for the backend.
func (a *outputApply) request(req ports.OutputApply) {
	if a.running || a.pending {
		a.reply(req.ID, errors.New("output apply already pending"))
		return
	}
	previous := make(map[string]ports.OutputConfig, len(a.state.overrides))
	maps.Copy(previous, a.state.overrides)
	cfg, err := a.state.apply(req)
	if err != nil || req.Test {
		a.reply(req.ID, err)
		return
	}
	a.setConfig(cfg)
	if a.state.headless {
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
		a.state.overrides = a.previous
		cfg := a.state.effective()
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
	return ch, a.state.heads
}

// headsSent records that Wayland received the latest inventory.
func (a *outputApply) headsSent() { a.headsDue = false }

// replySent drops the delivered reply.
func (a *outputApply) replySent() {
	a.replies[0] = ports.OutputApplied{}
	a.replies = a.replies[1:]
}
