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
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-reloads:
			next := state.reload(ev.Config)
			select {
			case configs <- ports.ConfigChanged{Config: next}:
			case <-ctx.Done():
				return
			}
			if !state.headless {
				select {
				case backend <- next:
				case <-ctx.Done():
					return
				}
				select {
				case <-backendDone:
				case <-ctx.Done():
					return
				}
			}
		case heads := <-inventory:
			state.heads = heads
		case req := <-apply:
			previous := make(map[string]ports.OutputConfig, len(state.overrides))
			for name, entry := range state.overrides {
				previous[name] = entry
			}
			next, err := state.apply(req)
			if err == nil && !req.Test {
				select {
				case configs <- ports.ConfigChanged{Config: next}:
				case <-ctx.Done():
					return
				}
				if !state.headless {
					select {
					case backend <- next:
					case <-ctx.Done():
						return
					}
					select {
					case err = <-backendDone:
					case <-ctx.Done():
						return
					}
					if err != nil {
						state.overrides = previous
						prev := state.effective()
						select {
						case configs <- ports.ConfigChanged{Config: prev}:
						case <-ctx.Done():
							return
						}
						select {
						case backend <- prev:
						case <-ctx.Done():
							return
						}
						select {
						case <-backendDone:
						case <-ctx.Done():
							return
						}
					}
				}
				if err == nil {
					for _, h := range req.Heads {
						log.Info().Str("output", h.Name).Bool("enabled", h.Enabled).Float64("scale", h.Scale).Msg("output configuration applied")
					}
				}
			}
			if err != nil {
				log.Warn().Err(err).Msg("output configuration failed")
			}
			select {
			case replies <- ports.OutputApplied{ID: req.ID, Err: err}:
			case <-ctx.Done():
				return
			}
		}
	}
}
