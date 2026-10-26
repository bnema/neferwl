// Package layout owns column geometry, lane state and layout engine hooks.
// All mutable state belongs to a single owner goroutine. It depends only on
// domain geometry and port values, never workspace presentation or plumbing.
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
