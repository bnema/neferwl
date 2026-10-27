package layout

import (
	"github.com/bnema/neferwl/internal/core/geom"
	"github.com/bnema/neferwl/internal/ports"
)

// WindowID identifies a window without owning a client connection.
type WindowID = ports.WindowID

// Rect is a logical-pixel rectangle.
type Rect = ports.Rect

// Placement describes a window's presentation geometry.
type Placement = geom.Placement

func abs(n int) int { return max(n, -n) }
