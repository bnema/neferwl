package core

import (
	"slices"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// spawnPlacement owns pending SlotEnv claims. Slot and terminal retry decisions
// remain with their respective policies; only this registry mints and claims tokens.
type spawnPlacement struct {
	tokens  ports.TokenSource
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

func newSpawnPlacement(tokens ports.TokenSource) spawnPlacement {
	return spawnPlacement{tokens: tokens, pending: make(map[string]spawnTarget), slots: make(map[slotKey]string)}
}

// request mints a token and records its pending destination.
func (p *spawnPlacement) request(argv []string, target spawnTarget) domain.SpawnRequest {
	token := p.tokens.Token()
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
// follow reports a window a rule sent to another output that takes the focus
// there (placeByRule).
func (p *spawnPlacement) place(c *Core, v ports.WindowMapped) (follow bool) {
	path := "focused"
	defer func() { c.placementPath = path }()
	if v.Floating {
		if s, _ := c.screenOf(v.ID); s != nil {
			return false
		}
		// A dialog opens on its parent's workspace, over it, fullscreen too.
		if ps, pw := c.screenOf(v.Parent); v.Parent != 0 && pw != nil {
			path = "dialog"
			pw.AddDialog(v.ID, v.Parent, v.Width, v.Height)
			pw.SetModal(v.ID, v.Modal)
			ps.mon.normalize()
			return false
		}
		// Rules come after the dialog: it stays over its parent.
		eff, ok := c.ruleFor(v)
		if eff.output == "" && eff.workspace == "" && eff.number == 0 {
			if sc, w := c.ancestorWorkspace(v); w != nil {
				path = "lineage"
				return c.placeOnWorkspace(v, eff, sc, w, true)
			}
		}
		if ok {
			path = "rule"
			return c.placeByRule(v, eff, true)
		}
		if !c.placeCoverApp(v, c.cur(), c.cur().mon.Current(), 0) {
			c.cur().mon.AddFloating(v.ID, v.Width, v.Height)
		}
		return false
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
						path = "slot"
						if target.kind == spawnStash {
							target.ws.StashSpawned(v.ID)
						} else {
							target.ws.AddWindow(v.ID)
						}
						s.mon.normalize()
						return false
					}
				}
			case spawnSlot:
				if st := c.slots[target.slot]; st != nil && st.window == 0 {
					if sc, w := c.byName(target.slot.workspace); w != nil {
						path = "slot"
						p.dropSlot(target.slot)
						st.window = v.ID
						w.AddSlotWindow(v.ID, target.slot.index, st.cells)
						sc.mon.normalize()
						return false
					}
				}
			}
		}
	}
	if s, _ := c.screenOf(v.ID); s == nil {
		eff, ok := c.ruleFor(v)
		byRule := ok
		if eff.output == "" && eff.workspace == "" && eff.number == 0 {
			if sc, w := c.ancestorWorkspace(v); w != nil {
				path = "lineage"
				return c.placeOnWorkspace(v, eff, sc, w, true)
			}
		}
		// A window mapping at an output's rect goes to that output unless a
		// rule names an output or workspace; it opens there quietly.
		if v.HasAt && !v.Remap && eff.output == "" && eff.workspace == "" && eff.number == 0 {
			if i := c.outputAt(float64(v.AtX), float64(v.AtY)); i >= 0 && i != c.focusScreen {
				eff.output, ok, byRule = c.screens[i].name(), true, false
			}
		}
		if ok {
			path = "rule"
			if !byRule {
				path = "position"
			}
			return c.placeByRule(v, eff, byRule)
		}
		if !c.placeCoverApp(v, c.cur(), c.cur().mon.Current(), 0) {
			c.cur().mon.AddWindow(v.ID)
		}
	}
	return false
}

// sameApp is pure policy: absent identifiers never establish app identity.
func sameApp(a, b ports.WindowMapped) bool {
	// Satellite X11 windows carry the bridge's PID, not the application's.
	// Native Wayland and built-in X11 windows carry real application PIDs.
	return !a.PIDShared && !b.PIDShared && a.PID != 0 && a.PID == b.PID || a.AppID != "" && a.AppID == b.AppID
}

// placeCoverApp handles ordinary same-app arrivals on the workspace in view.
func (c *Core) placeCoverApp(v ports.WindowMapped, sc *screen, w *Workspace, cells int) bool {
	cover := w.Cover()
	if cover == 0 || !sameApp(v, c.windows.lookup(cover).client) {
		return false
	}
	w.AddAppWindow(v.ID, cells, v.Width, v.Height)
	sc.mon.normalize()
	return true
}

// ancestorWorkspace selects the nearest process ancestor with a placed window.
// Multiple windows of one process use the lowest window ID for stable placement.
func (c *Core) ancestorWorkspace(v ports.WindowMapped) (*screen, *Workspace) {
	if v.Remap || v.PIDShared {
		return nil, nil
	}
	for _, node := range v.Proc[min(1, len(v.Proc)):] {
		if node.Key.PID <= 1 {
			break
		}
		var chosen WindowID
		var sc *screen
		var ws *Workspace
		for id, rec := range c.windows.records {
			if id == v.ID || rec.client.PIDShared || len(rec.client.Proc) == 0 || rec.client.Proc[0].Key != node.Key {
				continue
			}
			if s, w := c.screenOf(id); w != nil && (ws == nil || id < chosen) {
				chosen, sc, ws = id, s, w
			}
		}
		if ws != nil {
			return sc, ws
		}
	}
	return nil, nil
}
