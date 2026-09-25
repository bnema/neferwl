package core

import (
	"crypto/rand"
	"slices"
	"time"

	"github.com/bnema/nefertty/internal/ports"
)

// The terminal is the desktop: a workspace on screen is never empty. When
// one is, core spawns the configured terminal with a SlotEnv token, and the
// window carrying it fills that workspace, wherever the focus is by then.

// termRetry is how long a terminal may take to map before another is spawned
// (the first one failed, or the terminal command is broken).
const termRetry = 5 * time.Second

type termSpawn struct {
	w  *Workspace
	at time.Time
}

// fillEmpty returns the terminals to spawn for empty workspaces on screen.
// Workspaces with slots are left to them.
func (c *Core) fillEmpty() []ports.SpawnRequest {
	if !c.ch.Terminal || len(c.cfg.Terminal.Command) == 0 {
		return nil
	}
	now := time.Now()
	var reqs []ports.SpawnRequest
	for _, s := range c.screens {
		w := s.mon.Current()
		if s.name() == "" || !w.empty() || c.hasSlots(w) || c.termPending(w, now) {
			continue
		}
		token := rand.Text()
		c.terms[token] = &termSpawn{w: w, at: now}
		reqs = append(reqs, ports.SpawnRequest{Argv: slices.Clone(c.cfg.Terminal.Command), Env: []string{ports.SlotEnv + "=" + token}})
	}
	return reqs
}

// termPending reports whether a recent terminal spawn waits for w; it drops
// expired ones.
func (c *Core) termPending(w *Workspace, now time.Time) bool {
	found := false
	for token, t := range c.terms {
		switch {
		case now.Sub(t.at) > termRetry:
			delete(c.terms, token)
		case t.w == w:
			found = true
		}
	}
	return found
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
