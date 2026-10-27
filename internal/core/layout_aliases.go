package core

import "github.com/bnema/neferwl/internal/core/layout"

// Column is a tiled stack; the layout package owns the representation.
type Column = layout.Column

// Overflow is the layout configuration key shared by core's config contracts.
type Overflow = layout.Overflow

// LayoutNames lists the registered configuration keys for config callers.
func LayoutNames() []string { return layout.LayoutNames() }
