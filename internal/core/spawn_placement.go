package core

import (
	"crypto/rand"
	"slices"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// spawnPlacement owns pending SlotEnv claims. Slot and terminal retry decisions
// remain with their respective policies; only this registry mints and claims tokens.
type spawnPlacement struct {
	pending map[string]spawnTarget
	slots   map[slotKey]string
}

// spawnKind tells what a pending spawn is destined for.
type spawnKind uint8

const (
	// spawnSlot fills a slot; the zero value.
	spawnSlot spawnKind = iota
	// spawnTerminal is an automatic terminal for an empty workspace.
	spawnTerminal
	// spawnStash is a window opened straight into the stash of ws.
	spawnStash
)

// spawnTarget identifies the destination of one pending spawn. slot is set
// for spawnSlot; ws and at for the workspace-bound kinds.
type spawnTarget struct {
	kind spawnKind
	slot slotKey
	ws   *Workspace
	at   time.Time
}

func newSpawnPlacement() spawnPlacement {
	return spawnPlacement{pending: make(map[string]spawnTarget), slots: make(map[slotKey]string)}
}

// request mints a token and records its pending destination.
func (p *spawnPlacement) request(argv []string, target spawnTarget) domain.SpawnRequest {
	token := rand.Text()
	p.pending[token] = target
	if target.kind == spawnSlot {
		p.slots[target.slot] = token
	}
	return domain.SpawnRequest{Argv: slices.Clone(argv), Env: []string{ports.SlotEnv + "=" + token}}
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

// workspacePending reports whether a workspace awaits a spawn of the given kind.
func (p *spawnPlacement) workspacePending(w *Workspace, kind spawnKind) bool {
	for _, target := range p.pending {
		if target.kind == kind && target.ws == w {
			return true
		}
	}
	return false
}

// expireWorkspaceClaims drops workspace-bound claims older than the retry interval.
func (p *spawnPlacement) expireWorkspaceClaims(now time.Time) {
	for token, target := range p.pending {
		if target.kind != spawnSlot && now.Sub(target.at) > termRetry {
			delete(p.pending, token)
		}
	}
}

// place claims a token once and puts its window in the target workspace.
// Without a slot, a window rule (rules.go) may choose its place; otherwise it
// opens on the focused screen's workspace.
// Untagged windows return before any map lookup or allocation.
func (p *spawnPlacement) place(c *Core, v ports.WindowMapped) {
	if v.Floating {
		if s, _ := c.screenOf(v.ID); s != nil {
			return
		}
		// A dialog opens on its parent's workspace, over it, fullscreen too.
		if ps, pw := c.screenOf(v.Parent); v.Parent != 0 && pw != nil {
			pw.AddDialog(v.ID, v.Parent, v.Width, v.Height)
			pw.SetModal(v.ID, v.Modal)
			ps.mon.normalize()
			return
		}
		// Rules come after the dialog: it stays over its parent.
		if eff, ok := c.ruleFor(v); ok {
			c.placeByRule(v, eff)
			return
		}
		c.cur().mon.AddFloating(v.ID, v.Width, v.Height)
		return
	}
	if v.Slot != "" {
		// A claim no window answered in time no longer places one.
		p.expireWorkspaceClaims(c.now())
		if target, ok := p.pending[v.Slot]; ok {
			switch target.kind {
			case spawnTerminal, spawnStash:
				delete(p.pending, v.Slot)
				for _, s := range c.screens {
					if s.mon.has(target.ws) {
						if target.kind == spawnStash {
							target.ws.StashSpawned(v.ID)
						} else {
							target.ws.AddWindow(v.ID)
						}
						s.mon.normalize()
						return
					}
				}
			case spawnSlot:
				if st := c.slots[target.slot]; st != nil && st.window == 0 {
					if sc, w := c.byName(target.slot.workspace); w != nil {
						p.dropSlot(target.slot)
						st.window = v.ID
						w.AddSlotWindow(v.ID, target.slot.index, st.cells)
						sc.mon.normalize()
						return
					}
				}
			}
		}
	}
	if s, _ := c.screenOf(v.ID); s == nil {
		if eff, ok := c.ruleFor(v); ok {
			c.placeByRule(v, eff)
			return
		}
		c.cur().mon.AddWindow(v.ID)
	}
}
