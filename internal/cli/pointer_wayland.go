package cli

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"bnema.dev/wlturbo"
	clientcore "github.com/bnema/go-wayland-bindings/client/wayland"
	clientvp "github.com/bnema/go-wayland-bindings/client/wlrvirtualpointer"
)

// pointerSession is a connected virtual pointer.
type pointerSession struct {
	c     *wlturbo.Display
	vp    *clientvp.VirtualPointer
	start time.Time
}

// dialPointer connects to the session display, resolved as for `neferwl
// state` ($WAYLAND_DISPLAY in $XDG_RUNTIME_DIR), and creates a virtual
// pointer, bound to the named output when output is set.
func dialPointer(output string) (*pointerSession, error) {
	if os.Getenv("XDG_RUNTIME_DIR") == "" || os.Getenv("WAYLAND_DISPLAY") == "" {
		return nil, errors.New("XDG_RUNTIME_DIR and WAYLAND_DISPLAY must be set")
	}
	c, err := wlturbo.Connect("")
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*pointerSession, error) {
		_ = c.Close()
		return nil, err
	}
	if err := c.Roundtrip(); err != nil {
		return fail(err)
	}
	reg := c.Registry()
	manager := clientvp.NewVirtualPointerManager(c.Context())
	version, err := reg.BindNegotiated(clientvp.VirtualPointerManagerInterface, 2, manager)
	if err != nil {
		return fail(fmt.Errorf("the compositor offers no virtual pointer: %w", err))
	}
	seat := clientcore.NewSeat(c.Context())
	if _, err := reg.BindNegotiated(clientcore.SeatInterface, 1, seat); err != nil {
		return fail(err)
	}
	var vp *clientvp.VirtualPointer
	if output == "" {
		vp, err = manager.CreateVirtualPointer(seat)
	} else {
		if version < 2 {
			return fail(errors.New("--output needs zwlr_virtual_pointer_manager_v1 version 2"))
		}
		var out *clientcore.Output
		if out, err = findOutput(c, output); err != nil {
			return fail(err)
		}
		vp, err = manager.CreateVirtualPointerWithOutput(seat, out)
	}
	if err != nil {
		return fail(err)
	}
	return &pointerSession{c: c, vp: vp, start: time.Now()}, nil
}

// findOutput binds every wl_output (v4, for names) and returns the named one.
func findOutput(c *wlturbo.Display, name string) (*clientcore.Output, error) {
	var found *clientcore.Output
	var names []string
	for gname, g := range c.Registry().GetGlobals() {
		if g.Interface != clientcore.OutputInterface || g.Version < 4 {
			continue
		}
		o := clientcore.NewOutput(c.Context())
		if err := c.Registry().Bind(gname, g.Interface, 4, o); err != nil {
			return nil, err
		}
		o.OnName(func(n string) {
			names = append(names, n)
			if n == name {
				found = o
			}
		})
	}
	if err := c.Roundtrip(); err != nil {
		return nil, err
	}
	if found == nil {
		return nil, usagef("no output %q (outputs: %v)", name, names)
	}
	return found, nil
}

func (p *pointerSession) now() uint32 { return uint32(time.Since(p.start).Milliseconds()) }

func fixed(v float64) wlturbo.Fixed { return wlturbo.NewFixed(v) }

func (p *pointerSession) frame() error { return p.vp.Frame() }

func (p *pointerSession) button(b uint32, down bool) error {
	state := uint32(0)
	if down {
		state = 1
	}
	if err := p.vp.Button(p.now(), b, state); err != nil {
		return err
	}
	return p.frame()
}

func (p *pointerSession) move(dx, dy float64) error {
	if err := p.vp.Motion(p.now(), fixed(dx), fixed(dy)); err != nil {
		return err
	}
	return p.frame()
}

// sync waits until the compositor has handled every request so far.
func (p *pointerSession) sync() error { return p.c.Roundtrip() }

// sleep waits d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

const absExtent = 1 << 16

func (p *pointerSession) run(ctx context.Context, o pointerOptions, a pointerAction) error {
	switch a.kind {
	case "abs":
		if err := p.vp.MotionAbsolute(p.now(), uint32(math.Round(a.x*absExtent)), uint32(math.Round(a.y*absExtent)), absExtent, absExtent); err != nil {
			return err
		}
		return p.frame()
	case "move":
		return p.move(a.x, a.y)
	case "press":
		return p.button(a.button, true)
	case "release":
		return p.button(a.button, false)
	case "click", "hold":
		d := o.clickDuration
		if a.kind == "hold" {
			d = o.holdDuration
			if a.duration > 0 {
				d = a.duration
			}
		}
		if err := p.button(a.button, true); err != nil {
			return err
		}
		if err := p.sync(); err != nil {
			return err
		}
		if err := sleep(ctx, d); err != nil {
			return err
		}
		return p.button(a.button, false)
	case "drag":
		if err := p.button(a.button, true); err != nil {
			return err
		}
		sx, sy := a.x/float64(o.dragSteps), a.y/float64(o.dragSteps)
		for i := 0; i < o.dragSteps; i++ {
			if err := p.sync(); err != nil {
				return err
			}
			if err := sleep(ctx, o.dragStepDelay); err != nil {
				return err
			}
			if err := p.move(sx, sy); err != nil {
				return err
			}
		}
		if err := p.sync(); err != nil {
			return err
		}
		return p.button(a.button, false)
	case "scroll":
		if err := p.vp.AxisSource(uint32(clientcore.AXIS_SOURCE_WHEEL)); err != nil {
			return err
		}
		n := a.steps
		if err := p.vp.AxisDiscrete(p.now(), a.axis, fixed(15*float64(n)), int32(n)); err != nil {
			return err
		}
		return p.frame()
	}
	return fmt.Errorf("unknown action %q", a.kind)
}

// runPointer runs actions in order. Buttons still pressed at the end are
// released by the compositor when the virtual pointer goes away.
func runPointer(ctx context.Context, o pointerOptions, actions []pointerAction) (err error) {
	p, err := dialPointer(o.output)
	if err != nil {
		return err
	}
	defer func() {
		// Destroying the pointer releases held buttons (protocol release on
		// disconnect); the roundtrip makes sure the compositor saw it all.
		derr := p.vp.Destroy()
		if derr == nil {
			derr = p.sync()
		}
		if cerr := p.c.Close(); derr == nil {
			derr = cerr
		}
		if err == nil {
			err = derr
		}
	}()
	for _, a := range actions {
		if err := p.run(ctx, o, a); err != nil {
			return fmt.Errorf("%s: %w", a, err)
		}
	}
	return p.sync()
}
