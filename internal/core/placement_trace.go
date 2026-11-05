package core

import (
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func (c *Core) tracePlacement(v ports.WindowMapped, before map[*screen]WindowID, seat, cover WindowID) {
	if c.ch.Placements == nil {
		return
	}
	sc, ws := c.screenOf(v.ID)
	if ws == nil {
		return
	}
	focus, _ := sc.mon.Focused()
	t := domain.PlacementTrace{Window: v.ID, Path: c.placementPath, Output: sc.name(), Workspace: ws.ID, Cover: cover, WorkspaceFocusChanged: before[sc] != focus, SeatFocusChanged: seat != c.keyboardFocus()}
	t.Launcher = c.launcherOf(v.ID)

	select {
	case c.ch.Placements <- t:
	default:
	}
}
