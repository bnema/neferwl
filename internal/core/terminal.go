package core

import (
	"crypto/rand"
	"slices"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// The terminal is the desktop: a workspace on screen is never empty. When
// one is, core spawns the configured terminal with a SlotEnv token, and the
// window carrying it fills that workspace, wherever the focus is by then.

// termRetry is the least time between two terminals for one workspace: a
// terminal that fails, or maps and exits at once, is not respawned in a loop.
const termRetry = 5 * time.Second

type termSpawn struct {
	w  *Workspace
	at time.Time
}

// keepsTerminal reports whether empty workspaces get a terminal.
func (c *Core) keepsTerminal() bool { return c.ch.Terminal && len(c.cfg.Terminal.Command) > 0 }

// fillEmpty returns the terminals to spawn for empty workspaces on screen.
// Workspaces with slots are left to them.
func (c *Core) fillEmpty() []ports.SpawnRequest {
	now := time.Now()
	// Expired spawns go first, whatever is on screen: pending tokens keep
	// wayland reading /proc on every map.
	for token, t := range c.terms {
		if now.Sub(t.at) > termRetry {
			delete(c.terms, token)
		}
	}
	if !c.keepsTerminal() {
		return nil
	}
	var reqs []ports.SpawnRequest
	for _, s := range c.screens {
		w := s.mon.Current()
		if s.name() == "" || !w.empty() || c.hasSlots(w) || c.termPending(w) || now.Sub(w.termAt) < termRetry {
			continue
		}
		token := rand.Text()
		w.termAt = now
		c.terms[token] = &termSpawn{w: w, at: now}
		reqs = append(reqs, ports.SpawnRequest{Argv: slices.Clone(c.cfg.Terminal.Command), Env: []string{ports.SlotEnv + "=" + token}})
	}
	return reqs
}

// termPending reports whether a terminal spawn waits for w.
func (c *Core) termPending(w *Workspace) bool {
	for _, t := range c.terms {
		if t.w == w {
			return true
		}
	}
	return false
}

func (c *Core) hasSlots(w *Workspace) bool {
	for key := range c.slots {
		if w.Name != "" && key.workspace == w.Name {
			return true
		}
	}
	return false
}

// placeTerminal puts the window of a pending terminal on its workspace. It
// returns false for any other window, or when the workspace is gone.
func (c *Core) placeTerminal(id WindowID, token string) bool {
	t := c.terms[token]
	if t == nil {
		return false
	}
	delete(c.terms, token)
	for _, s := range c.screens {
		if slices.Contains(s.mon.all(), t.w) {
			t.w.AddWindow(id)
			s.mon.normalize()
			return true
		}
	}
	return false
}
