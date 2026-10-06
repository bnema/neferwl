package core

import (
	"strings"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// Automatic terminal requests use a SlotEnv token to place their windows
// on the intended workspace even if focus changes before mapping.

// termRetry is the least time between two terminals for one workspace: a
// terminal that fails, or maps and exits at once, is not respawned in a loop.
const termRetry = 5 * time.Second

// keepsTerminal reports whether empty workspaces get a terminal.
func (c *Core) keepsTerminal() bool {
	return c.opts.Terminal && c.cfg.Terminal.AutoOpen != "off" && len(c.cfg.Terminal.Command) > 0
}

// fillEmpty returns the terminals to spawn for empty workspaces on screen.
// Workspaces with slots are left to them.
func (c *Core) fillEmpty() []ports.SpawnRequest {
	now := c.now()
	c.placement.expireTerminals(now)
	if !c.keepsTerminal() {
		return nil
	}
	var reqs []ports.SpawnRequest
	for _, s := range c.screens {
		w := s.mon.Current()
		if c.cfg.Terminal.AutoOpen == "first" {
			if c.firstTerminalResolved || s != c.cur() || s.name() == "" {
				continue
			}
			c.firstTerminalResolved = true
			if w != s.mon.Workspaces[0] || !w.empty() || c.hasSlots(w) {
				continue
			}
		}
		if s.name() == "" || !w.empty() || c.hasSlots(w) || c.placement.terminalPending(w) || now.Sub(w.termAt) < termRetry {
			continue
		}
		c.firstTerminalResolved = true
		w.termAt = now
		reqs = append(reqs, c.placement.request(c.cfg.Terminal.Command, spawnTarget{terminal: w, at: now}))
	}
	return reqs
}

// cancelTerminal removes a claim minted for a send suppressed by the gate.
func (c *Core) cancelTerminal(req ports.SpawnRequest) {
	for _, env := range req.Env {
		if token, ok := strings.CutPrefix(env, ports.SlotEnv+"="); ok {
			if target, ok := c.placement.pending[token]; ok && target.terminal != nil {
				target.terminal.termAt = time.Time{}
				delete(c.placement.pending, token)
			}
		}
	}
}

func (c *Core) hasSlots(w *Workspace) bool {
	for key := range c.slots {
		if w.Name != "" && key.workspace == w.Name {
			return true
		}
	}
	return false
}
