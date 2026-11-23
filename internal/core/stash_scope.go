package core

import "github.com/bnema/neferwl/internal/domain"

// lendStashes applies stash.scope. In output scope each monitor owns one
// stash, lent to its current workspace; in global scope the session owns
// one, lent to the current workspace of the holder screen. The other
// workspaces hand theirs over. A stash a scope change left behind returns
// to the scope in force. Idempotent and allocation-free: it runs before
// every frame, transition and wait. A previewing monitor (overview,
// switcher) is left alone, its selection being provisional, except that a
// non-empty shared stash is never left unlent: its windows would be in no
// workspace.
func (c *Core) lendStashes() {
	scope := c.cfg.Stash.Scope
	output, global := scope == domain.StashScopeOutput, scope == domain.StashScopeGlobal
	// Release stale lendees first, so no monitor drains a workspace still
	// seeing another stash.
	for _, sc := range c.screens {
		m := sc.mon
		l := m.lendee
		if l == nil || !m.holds(l) {
			if l != nil {
				l.Lend(nil)
			}
			m.lendee = nil
			continue
		}
		if !m.previewing() && (!output || l != m.Current()) {
			l.Lend(nil)
			m.lendee = nil
		}
	}
	if len(c.screens) == 0 {
		return
	}
	if !global && c.stashLendee == nil && c.stash.Len() == 0 {
		// Steady output or workspace scope.
		c.lendMonitors(output)
		return
	}
	// A session stash going global is set up from the focused screen.
	if global && c.stashLendee == nil && c.stash.Len() == 0 {
		c.stashHolder = c.cur().name()
	}
	hs := c.holderScreen()
	if l := c.stashLendee; l != nil && (!global || !hs.mon.holds(l) || !hs.mon.previewing() && l != hs.mon.Current()) {
		l.Lend(nil)
		c.stashLendee = nil
	}
	if !global {
		// Orphaned session stash: to the holder's monitor or workspace.
		if output {
			hs.mon.stash.Absorb(&c.stash, false)
		} else {
			hs.mon.Current().TakeStash(&c.stash)
		}
		c.lendMonitors(output)
		return
	}
	setup := c.stashLendee == nil && c.stash.Len() == 0
	prefer, moved := c.stash.Len() > 0, false
	focused := c.cur()
	var hcur *Workspace
	if !hs.mon.previewing() {
		hcur = hs.mon.Current()
	} else if c.stashLendee == nil && c.stash.Len() > 0 {
		// Never unlent: see lendMonitors.
		c.stashLendee = hs.mon.Current()
		c.stashLendee.Lend(&c.stash)
	}
	for _, sc := range c.screens {
		m := sc.mon
		if m.previewing() {
			continue
		}
		if m.stash.Len() > 0 {
			c.stash.Absorb(&m.stash, setup && sc == focused)
			moved = true
		}
		// In screen then workspace order; the holder's current workspace
		// keeps the selection.
		for _, w := range m.Workspaces {
			if w != c.stashLendee && w.GiveStash(&c.stash, prefer || w == hcur) {
				moved = true
			}
		}
		for _, w := range m.hidden {
			if w != c.stashLendee && w.GiveStash(&c.stash, prefer || w == hcur) {
				moved = true
			}
		}
	}
	if moved && c.stashLendee != nil {
		c.stashLendee.Reattach()
	}
	if hcur != nil && c.stashLendee != hcur {
		hcur.GiveStash(&c.stash, true)
		hcur.Lend(&c.stash)
		c.stashLendee = hcur
	}
}

// lendMonitors runs the per-monitor part of lendStashes in output or
// workspace scope.
func (c *Core) lendMonitors(output bool) {
	for _, sc := range c.screens {
		m := sc.mon
		if m.previewing() {
			// A stash released under a preview (its lendee left, a replug
			// took windows back) is lent at once: unlent, its windows would
			// be in no workspace. A lent one waits for the preview to end.
			switch {
			case m.stash.Len() == 0:
			case !output:
				m.Current().TakeStash(&m.stash)
			case m.lendee == nil:
				cur := m.Current()
				cur.Lend(&m.stash)
				m.lendee = cur
			}
			continue
		}
		cur := m.Current()
		if !output {
			if m.stash.Len() > 0 {
				cur.TakeStash(&m.stash)
			}
			continue
		}
		prefer, moved := m.stash.Len() > 0, false
		for _, w := range m.Workspaces {
			if w != cur && w.GiveStash(&m.stash, prefer) {
				moved = true
			}
		}
		for _, w := range m.hidden {
			if w != cur && w.GiveStash(&m.stash, prefer) {
				moved = true
			}
		}
		if moved && m.lendee == cur {
			cur.Reattach()
		}
		if m.lendee != cur {
			// Collected first, the current workspace's stash wins the
			// selection.
			cur.GiveStash(&m.stash, true)
			cur.Lend(&m.stash)
			m.lendee = cur
		}
	}
}

// holderScreen is the screen holding the session stash, the focused one
// when the holder is unset or gone; it records the fallback.
func (c *Core) holderScreen() *screen {
	for _, sc := range c.screens {
		if c.stashHolder != "" && sc.name() == c.stashHolder {
			return sc
		}
	}
	sc := c.cur()
	c.stashHolder = sc.name()
	return sc
}

// pullStash makes sc hold the session stash in global scope, explicit user
// actions only, and reports whether the holder changed. Elsewhere it does
// nothing.
func (c *Core) pullStash(sc *screen) bool {
	if c.cfg.Stash.Scope != domain.StashScopeGlobal || sc == nil || c.holderScreen() == sc {
		return false
	}
	c.stashHolder = sc.name()
	c.lendStashes()
	return true
}

// holds reports whether w is one of the monitor's workspaces.
func (m *Monitor) holds(w *Workspace) bool {
	for _, v := range m.Workspaces {
		if v == w {
			return true
		}
	}
	for _, v := range m.hidden {
		if v == w {
			return true
		}
	}
	return false
}

// returnStash brings back, in output scope, the stashed windows that left
// with s's monitor when it was unplugged (removeScreen tags them home).
// The monitors holding them give their stash back first, so no workspace
// keeps a stale view; lendStashes lends them again.
func (c *Core) returnStash(s *screen) {
	if c.cfg.Stash.Scope != domain.StashScopeOutput {
		return
	}
	for _, o := range c.screens {
		if o == s {
			continue
		}
		if l := o.mon.lendee; l != nil {
			l.Lend(nil)
			o.mon.lendee = nil
		}
		o.mon.stash.TakeHome(&s.mon.stash, s.mon.matches)
	}
}
