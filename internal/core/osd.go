package core

import "github.com/bnema/neferwl/internal/ports"

// osd is the owner's window information overlay state.
type osd struct {
	key           osdKey
	shown, pinned bool
	window        WindowID
	stats         ports.WindowStats
	seq           uint64
	panel         *ports.PanelContent
	before        ports.WindowStats
}

// osdReset disarms recognition without retaining keys from another session.
func (c *Core) osdReset() {
	c.osd.key.stopHold()
	c.osd.key = osdKey{clock: c.opts.Clock}
	c.osd.key.setPinned(c.osd.pinned)
}

// osdApply applies a gesture without consuming its underlying input event.
func (c *Core) osdApply(result osdKeyResult) bool {
	if !c.cfg.OSD.Enable || c.security.Protected {
		return false
	}
	before, pin := c.osd.shown, c.osd.pinned
	switch result {
	case osdShow:
		if c.keyboardFocus() != 0 {
			c.osd.shown = true
		}
	case osdHide:
		if !c.osd.pinned {
			c.osd.shown = false
		}
	case osdTogglePin:
		c.osd.pinned = !c.osd.pinned
		c.osd.shown = c.osd.pinned
	}
	c.osd.key.setPinned(c.osd.pinned)
	return before != c.osd.shown || pin != c.osd.pinned
}
