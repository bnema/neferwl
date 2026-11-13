package core

import (
	"context"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// hoverEligible checks guards shared by entry and delayed focus.
func (c *Core) hoverEligible(id WindowID) bool {
	if !c.cfg.Focus.FollowMouse || id == 0 || c.synthetic || c.grab != 0 || len(c.buttons) != 0 ||
		c.constraint.Mode != domain.ConstraintNone || c.constrained.Mode != domain.ConstraintNone ||
		c.hint.phase != hintIdle || c.switching() || c.swipe != nil || c.gesture.on {
		return false
	}
	s, w := c.screenOf(id)
	if s == nil || w != s.mon.Current() || s.mon.previewing() || !w.HoverFocusable(id) {
		return false
	}
	if f, _ := c.cur().mon.Focused(); c.keyboardFocus() != f {
		return false // a layer, grab or menu holds the keyboard
	}
	return true
}

// hoverEntry reports whether the pointer moving from (px, py) into id is a
// real entry that focus.follow-mouse turns into focus. A window that slid
// under a still cursor (layout change) is not: the previous point hit-tests
// to it in the current layout. Called only when the hit window changed.
func (c *Core) hoverEntry(id WindowID, px, py float64) bool {
	if !c.hoverEligible(id) {
		return false
	}
	if prev, _, _ := c.hit(px, py); prev == id {
		if c.hoverLocked {
			c.hoverLock = id
		}
		return false
	}
	if c.hoverLocked && id == c.hoverLock {
		return false
	}
	c.hoverLocked, c.hoverLock = false, 0
	return true
}

// pendingHover is owned by the core event loop; a nil channel disables it.
type pendingHover struct {
	target WindowID
	timer  ports.Timer
	timerC <-chan time.Time
}

func (c *Core) cancelHover() {
	if c.hover.timer != nil {
		c.hover.timer.Stop()
	}
	c.hover = pendingHover{}
}

func (c *Core) startHover(ctx context.Context, id WindowID) error {
	c.cancelHover()
	if c.cfg.Focus.FollowMouseDelay == 0 {
		return c.applyHover(ctx, id)
	}
	timer := c.opts.Clock.NewTimer(c.cfg.Focus.FollowMouseDelay)
	c.hover = pendingHover{target: id, timer: timer, timerC: timer.C()}
	return nil
}

func (c *Core) hoverTick(ctx context.Context) error {
	id := c.hover.target
	c.cancelHover()
	if c.pointer != id || !c.hoverEligible(id) || c.hoverLocked {
		return nil
	}
	// Surface input regions and overlays can change without re-hitting the
	// cached pointer target. Dwell only counts while the target still wins.
	if hit, _, _ := c.hit(c.cursorX, c.cursorY); hit != id {
		return nil
	}
	return c.applyHover(ctx, id)
}

func (c *Core) applyHover(ctx context.Context, id WindowID) error {
	sent := c.keyboard.sent
	if err := c.focusPointer(ctx, id, true); err != nil {
		return err
	}
	if c.keyboard.sent != sent {
		return c.rehit(ctx)
	}
	return nil
}
