package core

import (
	"context"

	"github.com/bnema/neferwl/internal/ports"
)

// x11Active tracks the X11-active window: the one Xwayland clients see with
// input focus and as _NET_ACTIVE_WINDOW. Wayland keyboard focus is apart.
type x11Active struct {
	// held is the window an output.<name>.keep-focus holds active while the
	// keyboard is elsewhere (0: none); it is configured Activated.
	held WindowID
	// sent is the last ports.X11Active sent; wayland starts with none.
	sent WindowID
	// stale forces the next send: a security epoch change may have
	// cleared the display's X11 focus (even a lock and unlock core never
	// saw protected), so sent no longer says what it holds.
	stale bool
}

// isX11 reports whether id is a mapped X11 (Xwayland) window.
func (c *Core) isX11(id WindowID) bool {
	rec := c.windows.records[id]
	return rec != nil && rec.client.Xwayland
}

// decideX11Active returns the X11-active window for the keyboard focus:
// the focus itself when it is an X11 window, else the window the first
// keep-focus output holds, else none. It records the held window.
func (c *Core) decideX11Active(focus WindowID) WindowID {
	prev := c.x11.held
	c.x11.held = 0
	if c.security.Protected {
		return 0
	}
	if focus != 0 && c.isX11(focus) {
		return focus
	}
	for _, o := range c.cfg.Outputs {
		if !o.KeepFocus {
			continue
		}
		i := c.screenIndex(o.Name)
		if i < 0 || o.Name == "" {
			continue
		}
		if id := c.keptX11(c.screens[i], prev); id != 0 {
			c.x11.held = id
			return id
		}
	}
	return 0
}

// keptX11 is the X11 window sc keeps active: its focused window when that
// is a shown X11 window, else prev while still shown there, else the most
// recently mapped shown X11 window. Hidden windows are not eligible.
func (c *Core) keptX11(sc *screen, prev WindowID) WindowID {
	view := sc.mon.viewport()
	eligible := func(p Placement) bool {
		return !p.Hidden && !p.Leaving && p.Preview == 0 && view.shows(p) && c.isX11(p.ID)
	}
	layout := sc.shownLayout()
	focused, _ := sc.mon.Focused()
	var best, kept WindowID
	for _, p := range layout {
		if !eligible(p) {
			continue
		}
		switch p.ID {
		case focused:
			return p.ID
		case prev:
			kept = p.ID
		}
		if best == 0 || c.windows.records[p.ID].mappedAt.After(c.windows.records[best].mappedAt) {
			best = p.ID
		}
	}
	if kept != 0 {
		return kept
	}
	return best
}

// syncX11Active tells wayland the X11-active window when it changed.
func (c *Core) syncX11Active(ctx context.Context, id WindowID) error {
	if id == c.x11.sent && !c.x11.stale {
		return nil
	}
	if err := c.command(ctx, ports.X11Active{ID: id}); err != nil {
		return err
	}
	c.x11.sent, c.x11.stale = id, false
	return nil
}
