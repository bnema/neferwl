package core

import "github.com/bnema/neferwl/internal/core/layout"

// Column is a tiled stack. Kept as an alias while Workspace and its callers
// still live in core; the layout package owns the representation.
type Column = layout.Column

// Overflow is the layout configuration key shared by core's config contracts.
type Overflow = layout.Overflow

const (
	// OverflowScroll selects scrolling columns.
	OverflowScroll = layout.OverflowScroll
	// OverflowFixed selects the fixed row and spiral.
	OverflowFixed = layout.OverflowFixed
	// OverflowCascade selects vertically scrolling bands.
	OverflowCascade = layout.OverflowCascade
)

// LayoutNames lists the registered configuration keys for config callers.
func LayoutNames() []string { return layout.LayoutNames() }
