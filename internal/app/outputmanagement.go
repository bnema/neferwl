package app

import (
	"context"
	"fmt"
	"image"
	"math"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

// outputOverrides owns runtime-only output settings. It is used only by the
// app's config/apply relay goroutine; the file configuration is never mutated.
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
func (o *outputOverrides) reload(file ports.Config) ports.Config {
	o.file = file
	clear(o.overrides)
	return o.effective()
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
		if change.Transform != 0 || change.AdaptiveSync != nil || change.CustomMode {
			return fmt.Errorf("unsupported output setting on %s", change.Name)
		}
		if change.Scale < 0 || change.Scale > 4 || (change.Scale > 0 && change.Scale < 1) || math.IsNaN(change.Scale) || math.IsInf(change.Scale, 0) {
			return fmt.Errorf("invalid scale on %s", change.Name)
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

// relayOutputSettings serializes reloads, inventory, and protocol requests.
// The app owns the only writer to overrides; core receives effective settings.
func relayOutputSettings(ctx context.Context, state *outputOverrides, reloads <-chan ports.ConfigChanged, inventory <-chan ports.OutputHeads, apply <-chan ports.OutputApply, configs chan<- ports.ConfigChanged, backend chan<- ports.Config, backendDone <-chan error, replies chan<- ports.OutputApplied, log zerowrap.Logger) {
	type operation struct {
		cfg      ports.Config
		req      *ports.OutputApply
		previous map[string]ports.OutputConfig
		rollback bool
		failure  error
	}
	var queue []operation
	var active *operation
	var sending chan<- ports.Config
	var next ports.Config
	var configSend chan<- ports.ConfigChanged
	var pendingConfig ports.ConfigChanged
	var pendingReplies []ports.OutputApplied
	publish := func(cfg ports.Config) {
		pendingConfig = ports.ConfigChanged{Config: cfg}
		configSend = configs
	}
	reply := func(id uint64, err error) {
		if err != nil {
			log.Warn().Err(err).Msg("output configuration failed")
		}
		pendingReplies = append(pendingReplies, ports.OutputApplied{ID: id, Err: err})
	}
	enqueue := func(op operation) {
		if state.headless {
			return
		}
		queue = append(queue, op)
	}
	for {
		if active == nil && len(queue) > 0 {
			op := queue[0]
			active = &op
			queue = queue[1:]
			next = active.cfg
			sending = backend
		}
		var replySend chan<- ports.OutputApplied
		var nextReply ports.OutputApplied
		if len(pendingReplies) > 0 {
			replySend = replies
			nextReply = pendingReplies[0]
		}
		select {
		case <-ctx.Done():
			return
		case configSend <- pendingConfig:
			configSend = nil
		case replySend <- nextReply:
			pendingReplies[0] = ports.OutputApplied{}
			pendingReplies = pendingReplies[1:]
		case sending <- next:
			sending = nil
		case ev := <-reloads:
			cfg := state.reload(ev.Config)
			publish(cfg)
			// The reload supersedes runtime overrides. Old backend completion cannot
			// roll back the new file configuration.
			if active != nil && active.req != nil {
				reply(active.req.ID, fmt.Errorf("output apply superseded by reload"))
				active.req = nil
			}
			for i := range queue {
				if queue[i].req != nil {
					reply(queue[i].req.ID, fmt.Errorf("output apply superseded by reload"))
				}
			}
			queue = nil // Only the latest reload needs backend work.
			if active != nil && sending != nil {
				// The backend has not accepted the active config yet.
				active.cfg = cfg
				next = cfg
			} else {
				enqueue(operation{cfg: cfg})
			}
		case heads := <-inventory:
			state.heads = heads
		case req := <-apply:
			if active != nil || len(queue) > 0 {
				reply(req.ID, fmt.Errorf("output apply already pending"))
				continue
			}
			previous := make(map[string]ports.OutputConfig, len(state.overrides))
			for name, entry := range state.overrides {
				previous[name] = entry
			}
			cfg, err := state.apply(req)
			if err != nil || req.Test {
				reply(req.ID, err)
				continue
			}
			publish(cfg)
			if state.headless {
				reply(req.ID, nil)
			} else {
				enqueue(operation{cfg: cfg, req: &req, previous: previous})
			}
		case err := <-backendDone:
			if active == nil || sending != nil {
				continue
			}
			op := active
			active = nil
			if op.req == nil {
				continue
			}
			if op.rollback {
				reply(op.req.ID, op.failure)
				continue
			}
			if err != nil {
				state.overrides = op.previous
				cfg := state.effective()
				publish(cfg)
				enqueue(operation{cfg: cfg, req: op.req, rollback: true, failure: err})
				continue
			}
			for _, h := range op.req.Heads {
				log.Info().Str("output", h.Name).Bool("enabled", h.Enabled).Float64("scale", h.Scale).Msg("output configuration applied")
			}
			reply(op.req.ID, nil)
		}
	}
}
