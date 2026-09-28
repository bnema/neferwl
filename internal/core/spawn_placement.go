package core

import (
	"crypto/rand"
	"slices"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// spawnPlacement owns pending SlotEnv claims. Slot and terminal retry decisions
// remain with their respective policies; only this registry mints and claims tokens.
type spawnPlacement struct {
	pending map[string]spawnTarget
	slots   map[slotKey]string
}

// spawnTarget identifies the destination of one pending spawn.
type spawnTarget struct {
	slot     slotKey
	terminal *Workspace
	at       time.Time
}

func newSpawnPlacement() spawnPlacement {
	return spawnPlacement{pending: make(map[string]spawnTarget), slots: make(map[slotKey]string)}
}

// request mints a token and records its pending destination.
func (p *spawnPlacement) request(argv []string, target spawnTarget) ports.SpawnRequest {
	token := rand.Text()
	p.pending[token] = target
	if target.terminal == nil {
		p.slots[target.slot] = token
	}
	return ports.SpawnRequest{Argv: slices.Clone(argv), Env: []string{ports.SlotEnv + "=" + token}}
}

// dropSlot invalidates the pending token of a slot.
func (p *spawnPlacement) dropSlot(key slotKey) {
	if token := p.slots[key]; token != "" {
		delete(p.pending, token)
		delete(p.slots, key)
	}
}

// slotPending reports whether a slot still awaits a window.
func (p *spawnPlacement) slotPending(key slotKey) bool { return p.slots[key] != "" }

// anyPending reports whether any spawn still awaits a window.
func (p *spawnPlacement) anyPending() bool { return len(p.pending) != 0 }

// terminalPending reports whether a workspace awaits an automatic terminal.
func (p *spawnPlacement) terminalPending(w *Workspace) bool {
	for _, target := range p.pending {
		if target.terminal == w {
			return true
		}
	}
	return false
}

// expireTerminals drops terminal claims older than the retry interval.
func (p *spawnPlacement) expireTerminals(now time.Time) {
	for token, target := range p.pending {
		if target.terminal != nil && now.Sub(target.at) > termRetry {
			delete(p.pending, token)
		}
	}
}

// place claims a token once and puts its window in the target workspace.
// Untagged windows return before any map lookup or allocation.
func (p *spawnPlacement) place(c *Core, v ports.WindowMapped) {
	if v.Floating {
		if s, _ := c.screenOf(v.ID); s == nil {
			c.cur().mon.AddFloating(v.ID, v.Width, v.Height)
		}
		return
	}
	if v.Slot != "" {
		if target, ok := p.pending[v.Slot]; ok {
			if target.terminal != nil {
				delete(p.pending, v.Slot)
				for _, s := range c.screens {
					if slices.Contains(s.mon.all(), target.terminal) {
						target.terminal.AddWindow(v.ID)
						s.mon.normalize()
						return
					}
				}
			} else if st := c.slots[target.slot]; st != nil && st.window == 0 {
				if sc, w := c.byName(target.slot.workspace); w != nil {
					p.dropSlot(target.slot)
					st.window = v.ID
					w.AddSlotWindow(v.ID, target.slot.index, st.width)
					sc.mon.normalize()
					return
				}
			}
		}
	}
	if s, _ := c.screenOf(v.ID); s == nil {
		c.cur().mon.AddWindow(v.ID)
	}
}
