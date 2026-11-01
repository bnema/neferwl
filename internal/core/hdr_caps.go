package core

import "github.com/bnema/neferwl/internal/domain"

// outputHDRCaps reads the latest snapshot. Only the core owner may call it.
func (c *Core) outputHDRCaps(name string) (domain.OutputHDRCaps, bool) {
	caps, ok := c.hdrCaps[name]
	return caps, ok
}
