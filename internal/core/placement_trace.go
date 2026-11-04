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
	if t.Path == "dialog" {
		t.Launcher = v.Parent
	}
	if t.Path == "lineage" {
		for _, pid := range v.AncestorPIDs {
			for id, rec := range c.windows.records {
				if id != v.ID && !rec.client.PIDShared && rec.client.PID == pid {
					if _, w := c.screenOf(id); w != nil && (t.Launcher == 0 || id < t.Launcher) {
						t.Launcher = id
					}
				}
			}
			if t.Launcher != 0 {
				break
			}
		}
	}
	select {
	case c.ch.Placements <- t:
	default:
	}
}
