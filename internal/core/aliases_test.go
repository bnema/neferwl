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
	// AnchorWindow is a drop zone on a tile.
	AnchorWindow = workspacedomain.AnchorWindow
	// AnchorGap is a drop zone between columns.
	AnchorGap = workspacedomain.AnchorGap
)

// DropBelow calls workspace.DropBelow.
func DropBelow(src, w *Workspace, id, anchor WindowID) { workspacedomain.DropBelow(src, w, id, anchor) }
