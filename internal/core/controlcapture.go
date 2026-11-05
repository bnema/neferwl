package core

import (
	"context"
	"path/filepath"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// controlCapture decides a continuous capture request and hands the backend
// a resolved plan: live output names and absolute per-output directories.
// The backend only fans it out, acknowledges and rolls back.
func (c *Core) controlCapture(ctx context.Context, r ports.ControlCapture) error {
	refuse := func(code string) error {
		select {
		case r.Reply <- ports.ContinuousCaptureResult{Dir: r.Dir, Err: code}:
		default:
		}
		return nil
	}
	switch {
	case r.Caps&domain.CapCapture == 0:
		return refuse(ports.ControlErrForbidden)
	case c.protectionRequested():
		return refuse(ports.ControlErrLocked)
	case c.ch.Continuous == nil:
		return refuse(ports.ControlErrBadRequest)
	case !r.Stop && !filepath.IsAbs(r.Dir):
		// Never relative to the compositor's working directory.
		return refuse(ports.ControlErrBadRequest)
	}
	names := r.Outputs
	if len(names) == 0 {
		for _, sc := range c.screens {
			if sc.name() != "" {
				names = append(names, sc.name())
			}
		}
	}
	if len(names) == 0 {
		return refuse(ports.ControlErrBadRequest)
	}
	plan := ports.ContinuousCaptureRequest{Dir: r.Dir, Raw: r.Raw, Stop: r.Stop, Reply: r.Reply, Targets: make([]ports.ContinuousTarget, 0, len(names))}
	for _, name := range names {
		if c.screenIndex(name) < 0 {
			return refuse(ports.ControlErrBadRequest)
		}
		target := ports.ContinuousTarget{Output: name}
		if !r.Stop {
			// Several outputs share the directory: one subdirectory each.
			target.Dir = filepath.Clean(r.Dir)
			if len(names) > 1 {
				target.Dir = filepath.Join(target.Dir, name)
			}
		}
		plan.Targets = append(plan.Targets, target)
	}
	select {
	case c.ch.Continuous <- plan:
	case <-ctx.Done():
		return ctx.Err()
	default:
		// The backend is busy with earlier plans: refuse rather than stall.
		return refuse(ports.ControlErrBusy)
	}
	return nil
}
