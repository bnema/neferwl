package core

import "github.com/bnema/neferwl/internal/domain"

// hoverEntry reports whether the pointer moving from (px, py) into id is a
// real entry that focus.follow-mouse turns into focus. A window that slid
// under a still cursor (layout change) is not: the previous point hit-tests
// to it in the current layout. Called only when the hit window changed.
func (c *Core) hoverEntry(id WindowID, px, py float64) bool {
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
