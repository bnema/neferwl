package core

import "github.com/bnema/neferwl/internal/domain"

// lendStashes applies stash.scope. In output scope each monitor owns one
// stash, lent to its current workspace; the other workspaces of the
// monitor hand theirs over. In workspace scope a monitor stash left by a
// scope change returns to the current workspace. Idempotent and
// allocation-free: it runs before every frame, transition and wait. A
// previewing monitor (overview, switcher) is left alone: its selection is
// provisional and a handoff could not be undone.
func (c *Core) lendStashes() {
	output := c.cfg.Stash.Scope == domain.StashScopeOutput
	// Release stale lendees first, so no monitor drains a workspace still
	// seeing another monitor's stash.
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
	for _, sc := range c.screens {
		m := sc.mon
		if m.previewing() {
			continue
		}
		cur := m.Current()
		if !output {
			if m.stash.Len() > 0 {
				cur.TakeStash(&m.stash)
			}
			continue
		}
		prefer := m.stash.Len() > 0
		for _, w := range m.Workspaces {
			if w != cur {
				w.GiveStash(&m.stash, prefer)
			}
		}
		for _, w := range m.hidden {
			if w != cur {
				w.GiveStash(&m.stash, prefer)
			}
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
