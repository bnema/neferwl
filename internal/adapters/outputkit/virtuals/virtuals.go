// Package virtuals hosts config-declared (SDR or HDR) outputs in a backend's existing set.
package virtuals

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"bnema.dev/zerowrap"
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
	head       domain.OutputHead
	hdr        domain.OutputHDRSettings
	continuous chan ports.OutputContinuous
	modes      chan headless.ModeRequest
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
		mode, err := domain.ParseOutputMode(cfg.Mode)
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
		if current != nil {
			select {
			case <-h.set.Outs[cfg.Name].Done:
				if err := h.Remove(ctx, cfg.Name); err != nil {
					result = errors.Join(result, err)
				}
				current = nil
			default:
			}
		}
		hdr := cfg.HDRSettings()
		if current != nil && current.hdr != hdr {
			// Like a DRM restart, the screen stays: the owner rebuilds its
			// renderer (enable, SDR white) or republishes caps (peak).
			if err := h.request(ctx, cfg.Name, current, headless.ModeRequest{Mode: *current.head.Current, HDR: &hdr}); err != nil {
				return errors.Join(result, err, h.Remove(ctx, cfg.Name))
			}
			current.hdr = hdr
		}
		if current == nil {
			if h.set.Outs[cfg.Name] != nil {
				return errors.Join(result, fmt.Errorf("output %s already running", cfg.Name))
			}
			continuous := make(chan ports.OutputContinuous)
			modes := make(chan headless.ModeRequest)
			cur := &headless.Cursor{}
			h.curs.Set(cfg.Name, cur)
			opts := headless.Options{Continuous: continuous, Name: cfg.Name, Cursor: cur, LoadCursor: xcursor.LoadCursor, NewRenderer: h.renderer, NewCaptureRenderer: h.renderer, Log: h.log, Presented: h.ch.Presented, Frames: h.ch.Frames, Captured: h.ch.Captured, Formats: h.ch.Formats, Events: h.ch.Events, ModeRequests: modes, HDR: hdr}
			opts.SetMode(mode.Width, mode.Height, mode.RefreshMilli)
			if !headless.StartOutput(ctx, h.set, h.ch, info, opts) {
				h.curs.Set(cfg.Name, nil)
				return errors.Join(result, fmt.Errorf("start virtual output %s failed", cfg.Name), h.set.StartErr, ctx.Err())
			}
			current = &output{continuous: continuous, modes: modes, hdr: hdr}
			h.outputs[cfg.Name] = current
			h.order = append(h.order, cfg.Name)
		} else if current.head.Info.Width != info.Width || current.head.Info.Height != info.Height || current.head.Info.RefreshMilli != info.RefreshMilli {
			// Inventory changes only after the owner has applied the mode.
			if err := h.request(ctx, cfg.Name, current, headless.ModeRequest{Mode: mode}); err != nil {
				return errors.Join(result, err, h.Remove(ctx, cfg.Name))
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

// request hands a mode or HDR change to the running owner and waits until
// it applied it.
func (h *Host) request(ctx context.Context, name string, o *output, req headless.ModeRequest) error {
	ack := make(chan error, 1)
	req.Result = ack
	select {
	case o.modes <- req:
	case <-h.set.Outs[name].Done:
		return fmt.Errorf("virtual output %s stopped", name)
	case <-ctx.Done():
		return ctx.Err()
	}
	return awaitMode(ctx, h.set.Outs[name].Done, ack, name)
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

// awaitMode retains the owner's result when completion and acknowledgement are
// both ready. The owner sends its buffered acknowledgement before closing Done.
func awaitMode(ctx context.Context, done <-chan struct{}, ack <-chan error, name string) error {
	select {
	case err := <-ack:
		return err
	case <-done:
		select {
		case err := <-ack:
			return err
		default:
			return fmt.Errorf("virtual output %s stopped applying mode", name)
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Continuous returns the writer channel of a live virtual output.
func (h *Host) Continuous(name string) chan<- ports.OutputContinuous {
	if o := h.outputs[name]; o != nil {
		return o.continuous
	}
	return nil
}
