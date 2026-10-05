package core

import (
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// Helpers shared by the transition tests (transition_*_internal_test.go).

// settleShown publishes so the screens' shown layouts are current.
func settleShown(t *testing.T, c *Core) {
	t.Helper()
	indicatorScene(t, c)
}

// settledOf is the settled placement of window id on sc.
func settledOf(t *testing.T, sc *screen, id WindowID) Placement {
	t.Helper()
	i := slices.IndexFunc(sc.settledLayout, func(p Placement) bool { return p.ID == id })
	if i < 0 {
		t.Fatalf("window %d not in the settled layout %+v", id, sc.settledLayout)
	}
	return sc.settledLayout[i]
}

// configuresOf returns the configures sent since the last drain, by window.
func configuresOf(cmds chan ports.ClientCommand) map[WindowID][]ports.ConfigureWindow {
	out := map[WindowID][]ports.ConfigureWindow{}
	for {
		select {
		case cmd := <-cmds:
			if v, ok := cmd.(ports.ConfigureWindow); ok {
				out[v.ID] = append(out[v.ID], v)
			}
		default:
			return out
		}
	}
}

// drainConfigures returns the IDs configured since the last drain, in order.
func drainConfigures(cmds chan ports.ClientCommand) []WindowID {
	var ids []WindowID
	for {
		select {
		case cmd := <-cmds:
			if v, ok := cmd.(ports.ConfigureWindow); ok {
				ids = append(ids, v.ID)
			}
		default:
			return ids
		}
	}
}
