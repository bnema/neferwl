package core

import (
	"github.com/bnema/neferwl/internal/core/layout"
	workspacedomain "github.com/bnema/neferwl/internal/core/workspace"
)

// Aliases for values that only core's tests spell through the core package.
const (
	// OverflowScroll selects scrolling columns.
	OverflowScroll = layout.OverflowScroll
	// OverflowFixed selects the fixed row and spiral.
	OverflowFixed = layout.OverflowFixed
	// OverflowCascade selects vertically scrolling bands.
	OverflowCascade = layout.OverflowCascade
	// ResizeWidth is the resize axis of set-column-width.
	ResizeWidth = workspacedomain.ResizeWidth
	// ResizeHeight is the resize axis of set-window-height.
	ResizeHeight = workspacedomain.ResizeHeight
	// DropBelow drops a window below its anchor.
	DropBelow = workspacedomain.DropBelow
	// DropSwap swaps a window with its anchor.
	DropSwap = workspacedomain.DropSwap
	// DropInsert inserts a window as a column beside its anchor.
	DropInsert = workspacedomain.DropInsert
)
