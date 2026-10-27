package layout

import (
	"github.com/bnema/neferwl/internal/core/geom"
	"github.com/bnema/neferwl/internal/domain"
)

// WindowID identifies a window without owning a client connection.
type WindowID = domain.WindowID

// Rect is a logical-pixel rectangle.
type Rect = domain.Rect

// Placement describes a window's presentation geometry.
type Placement = geom.Placement

func abs(n int) int { return max(n, -n) }
