// Package virtuals hosts config-declared SDR outputs in a backend's existing set.
package virtuals

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"bnema.dev/zerowrap"
	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/headless"
	"github.com/bnema/neferwl/internal/adapters/outputkit/cursors"
	"github.com/bnema/neferwl/internal/adapters/outputkit/routing"
	"github.com/bnema/neferwl/internal/adapters/xcursor"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// Host is owned by the backend loop, along with Set. It never starts a router
// or consumes the backend's shared channels.
type Host struct {
	set      *routing.Set
	ch       routing.Channels
	curs     *cursors.Router
	renderer func(int, int) (ports.Renderer, error)
	log      zerowrap.Logger
	outputs  map[string]*output
	order    []string
}
type output struct {
	head  domain.OutputHead
	modes chan domain.OutputMode
}

func New(set *routing.Set, ch routing.Channels, curs *cursors.Router, renderer func(int, int) (ports.Renderer, error), log zerowrap.Logger) *Host {
	return &Host{set: set, ch: ch, curs: curs, renderer: renderer, log: log, outputs: map[string]*output{}}
}

// Sync reconciles core's desired set. Off entries are retained as disabled heads;
// their owner stays alive and core's scenes gate rendering, as for physical heads.
// Inputs are already validated by core/config; errors still fail closed here.
func (h *Host) Sync(ctx context.Context, want []domain.OutputConfig) error {
	desired := map[string]bool{}
	modes := make(map[string]domain.OutputMode, len(want))
	for _, cfg := range want {
		if cfg.Name == "" || desired[cfg.Name] {
			return fmt.Errorf("empty or duplicate virtual output name %q", cfg.Name)
		}
		mode, err := parseMode(cfg.Mode)
		if err != nil {
			return fmt.Errorf("virtual %s: %w", cfg.Name, err)
		}
		modes[cfg.Name] = mode
		desired[cfg.Name] = true
	}
	var result error
	for _, name := range slices.Clone(h.order) {
		if !desired[name] {
			result = errors.Join(result, h.Remove(ctx, name))
		}
	}
	for _, cfg := range want {
		mode := modes[cfg.Name]
		info := domain.OutputInfo{Name: cfg.Name, Virtual: true, Width: mode.Width, Height: mode.Height, RefreshMilli: mode.RefreshMilli}
		current := h.outputs[cfg.Name]
		if current == nil {
			if h.set.Outs[cfg.Name] != nil {
				return errors.Join(result, fmt.Errorf("output %s already running", cfg.Name))
			}
			modes := make(chan domain.OutputMode, 1)
			cur := &headless.Cursor{}
			h.curs.Set(cfg.Name, cur)
			opts := headless.Options{Name: cfg.Name, Cursor: cur, LoadCursor: xcursor.LoadCursor, NewRenderer: h.renderer, NewCaptureRenderer: h.renderer, Log: h.log, Presented: h.ch.Presented, Frames: h.ch.Frames, Captured: h.ch.Captured, Formats: h.ch.Formats, Events: h.ch.Events, Modes: modes}
			opts.SetMode(mode.Width, mode.Height, mode.RefreshMilli)
			if !headless.StartOutput(ctx, h.set, h.ch, info, opts) {
				h.curs.Set(cfg.Name, nil)
				return errors.Join(result, fmt.Errorf("start virtual output %s failed", cfg.Name), h.set.StartErr, ctx.Err())
			}
			current = &output{modes: modes}
			h.outputs[cfg.Name] = current
			h.order = append(h.order, cfg.Name)
		} else if current.head.Info.Width != info.Width || current.head.Info.Height != info.Height || current.head.Info.RefreshMilli != info.RefreshMilli {
			// FIFO mode delivery: an owner either consumes the change or exits.
			select {
			case current.modes <- mode:
			case <-h.set.Outs[cfg.Name].Done:
				return errors.Join(result, fmt.Errorf("virtual output %s stopped", cfg.Name))
			case <-ctx.Done():
				return errors.Join(result, ctx.Err())
			}
			select {
			case h.ch.Events <- ports.OutputAdded{Info: info}:
			case <-ctx.Done():
				return errors.Join(result, ctx.Err())
			}
		}
		current.head = domain.OutputHead{Info: info, Modes: []domain.OutputMode{mode}, Current: &mode, Enabled: !cfg.Off}
	}
	// Only reorder once reconciliation succeeds; partial errors retain all lifetimes.
	h.order = h.order[:0]
	for _, cfg := range want {
		h.order = append(h.order, cfg.Name)
	}
	return result
}

// Remove finishes one owned lifetime, including a spontaneously stopped owner.
// Backend stop notifications must be checked with Set.StoppedCurrent first.
func (h *Host) Remove(ctx context.Context, name string) error {
	if h.outputs[name] == nil {
		return nil
	}
	running := h.set.Outs[name]
	running.Stop()
	err := h.set.Finish(name)
	if !h.set.SecurityEvent(ports.SecurityOutputRemoved{Instance: running.Instance}) {
		err = errors.Join(err, h.set.Ctx.Err())
	}
	select {
	case h.ch.Events <- ports.OutputRemoved{Name: name}:
	case <-ctx.Done():
		err = errors.Join(err, ctx.Err())
	}
	h.curs.Set(name, nil)
	delete(h.outputs, name)
	h.order = slices.DeleteFunc(h.order, func(v string) bool { return v == name })
	return err
}

// Owns identifies stop notifications belonging to this host.
func (h *Host) Owns(name string) bool { return h.outputs[name] != nil }

// Heads returns an independent inventory snapshot in desired config order.
func (h *Host) Heads() []domain.OutputHead {
	heads := make([]domain.OutputHead, 0, len(h.order))
	for _, name := range h.order {
		head := h.outputs[name].head
		head.Modes = slices.Clone(head.Modes)
		if head.Current != nil {
			mode := *head.Current
			head.Current = &mode
		}
		heads = append(heads, head)
	}
	return heads
}

func parseMode(value string) (domain.OutputMode, error) {
	w, h, hz, err := config.ParseMode(value)
	if err != nil || w < 1 || h < 1 || w > 16384 || h > 16384 || math.IsNaN(hz) || math.IsInf(hz, 0) {
		return domain.OutputMode{}, fmt.Errorf("invalid mode %q", value)
	}
	if hz == 0 {
		hz = 60
	}
	refresh := math.Round(hz * 1000)
	if refresh < 1 || refresh >= float64(int(^uint(0)>>1)) {
		return domain.OutputMode{}, fmt.Errorf("invalid refresh %q", value)
	}
	return domain.OutputMode{Width: w, Height: h, RefreshMilli: int(refresh)}, nil
}
