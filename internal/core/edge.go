package core

import (
	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/domain"
)

type edgeKind uint8

const (
	edgeNone edgeKind = iota
	edgeScreen
	edgeWorkspace
)

type edgeHop struct {
	kind  edgeKind
	index int
}

// edgeTarget decides where focus goes past a content edge, without moving it.
func (c *Core) edgeTarget(axis layout.Axis, dir int, d direction) edgeHop {
	m := c.cur().mon
	step := layout.WorkspaceStep(m.Current().Engine(), axis, dir)
	// Screen eligibility follows the layout, even for a shown named workspace.
	// Cascade's vertical edges stay within its bands.
	screenOK := axis == layout.Horizontal || step != 0
	if m.shown != nil {
		step = 0
	}
	i := -1
	if screenOK {
		i = c.neighbor(d)
	}
	if axis == layout.Horizontal && i >= 0 {
		return edgeHop{kind: edgeScreen, index: i}
	}
	workspaceOK := c.cfg.Navigation.Edge == domain.NavEdgeWorkspace
	if to := m.occupiedStep(axis, dir); workspaceOK && to >= 0 {
		return edgeHop{kind: edgeWorkspace, index: to}
	}
	if i >= 0 {
		return edgeHop{kind: edgeScreen, index: i}
	}
	if workspaceOK && step != 0 && m.Active+step >= 0 && m.Active+step < len(m.Workspaces) {
		return edgeHop{kind: edgeWorkspace, index: m.Active + step}
	}
	return edgeHop{}
}

// edgeGo applies a decided hop.
func (c *Core) edgeGo(h edgeHop, d direction) bool {
	switch h.kind {
	case edgeScreen:
		c.focusScreen = h.index
		c.cur().mon.focusEntry(d)
	case edgeWorkspace:
		c.cur().mon.Focus(h.index)
	default:
		return false
	}
	return true
}
