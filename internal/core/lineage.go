package core

import (
	"slices"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

type lineageEdge uint8

const (
	lineageProcess lineageEdge = iota
	lineageDialog
)

// lineageRecord survives unmap; only the core owner goroutine accesses it.
type lineageRecord struct {
	launcher             WindowID
	edge                 lineageEdge
	proc                 []domain.ProcNode
	shared, mapped       bool
	focused, mappedOrder uint64
}

type lineage struct {
	records        map[WindowID]*lineageRecord
	order          uint64
	focusOrder     uint64
	seat           WindowID
	workspaceFocus map[*Workspace]WindowID
}

func (c *Core) launcherOf(id WindowID) WindowID {
	if r := c.lineage.records[id]; r != nil {
		return r.launcher
	}
	return 0
}

func (c *Core) mapLineage(v ports.WindowMapped) {
	l := &c.lineage
	if l.records == nil {
		l.records = make(map[WindowID]*lineageRecord)
	}
	l.order++
	if r := l.records[v.ID]; r != nil {
		r.mapped = true
		r.mappedOrder = l.order
		return
	}
	r := &lineageRecord{proc: slices.Clone(v.Proc), shared: v.PIDShared, mapped: true, mappedOrder: l.order}
	if !v.PIDShared {
		if _, w := c.screenOf(v.Parent); v.Parent != 0 && w != nil {
			r.launcher, r.edge = v.Parent, lineageDialog
		} else {
			r.launcher = c.resolveLauncher(v)
		}
	}
	l.records[v.ID] = r
}

func (c *Core) resolveLauncher(v ports.WindowMapped) WindowID {
	var best WindowID
	bestI, bestJ := int(^uint(0)>>1), int(^uint(0)>>1)
	consider := func(id WindowID, i, j int) {
		if i < bestI || i == bestI && (j < bestJ || j == bestJ && c.preferLauncher(id, best)) {
			best, bestI, bestJ = id, i, j
		}
	}
	// Direct ancestry always wins over a common-ancestor edge.
	for id, r := range c.lineage.records {
		if id == v.ID || !r.mapped || r.shared || len(r.proc) == 0 {
			continue
		}
		if _, w := c.screenOf(id); w == nil {
			continue
		}
		for i := 1; i < len(v.Proc); i++ {
			if v.Proc[i].Key == r.proc[0].Key {
				consider(id, i, 0)
				break
			}
		}
	}
	if best != 0 {
		return best
	}
	for id, r := range c.lineage.records {
		if id == v.ID || !r.mapped || r.shared {
			continue
		}
		if _, w := c.screenOf(id); w == nil {
			continue
		}
		for i := 1; i < len(v.Proc); i++ {
			if v.Proc[i].SessionLeader || c.lineageShell(v.Proc[i].Comm) {
				continue
			}
			for j := 1; j < len(r.proc); j++ {
				if !r.proc[j].SessionLeader && !c.lineageShell(r.proc[j].Comm) && v.Proc[i].Key == r.proc[j].Key && c.branchFocused(id, v.Proc[i-1].Started) {
					consider(id, i, j)
				}
			}
		}
	}
	return best
}

func (c *Core) preferLauncher(a, b WindowID) bool {
	if b == 0 {
		return true
	}
	focused := func(id WindowID) bool {
		_, w := c.screenOf(id)
		if w == nil {
			return false
		}
		f, _ := w.Focused()
		return f == id
	}
	af, bf := focused(a), focused(b)
	if af != bf {
		return af
	}
	ar, br := c.lineage.records[a], c.lineage.records[b]
	if ar.focused != br.focused {
		return ar.focused > br.focused
	}
	if ar.mappedOrder != br.mappedOrder {
		return ar.mappedOrder > br.mappedOrder
	}
	return a < b
}

func (c *Core) removeLineage(id WindowID, destroyed bool) {
	r := c.lineage.records[id]
	if r == nil {
		return
	}
	for child, cr := range c.lineage.records {
		if child != id && cr.launcher == id {
			cr.launcher = r.launcher
		}
	}
	r.mapped = false
	if destroyed {
		delete(c.lineage.records, id)
	}
}

// observeLineageFocus compares workspace-local state without allocating. Seat
// changes also count when switching to an already-focused workspace window.
func (c *Core) observeLineageFocus() {
	if c.lineage.workspaceFocus == nil {
		c.lineage.workspaceFocus = make(map[*Workspace]WindowID)
	}
	for _, sc := range c.screens {
		id, _ := sc.mon.Current().Focused()
		c.observeDomainFocus(sc, id)
		for _, w := range sc.mon.Workspaces {
			id, _ := w.Focused()
			if id != c.lineage.workspaceFocus[w] {
				c.lineage.workspaceFocus[w] = id
				c.noteLineageFocus(id)
			}
		}
	}
	// normalize and output removal can retire workspaces between observations.
	// Prune against the same live set we observe, without allocating scratch.
	for w := range c.lineage.workspaceFocus {
		live := false
		for _, sc := range c.screens {
			if slices.Contains(sc.mon.Workspaces, w) {
				live = true
				break
			}
		}
		if !live {
			delete(c.lineage.workspaceFocus, w)
		}
	}
	seat := c.keyboardFocus()
	if seat != c.lineage.seat {
		c.lineage.seat = seat
		c.observeDomainFocus(c.cur(), seat)
		c.noteLineageFocus(seat)
	}
}

func (c *Core) noteLineageFocus(id WindowID) {
	if r := c.lineage.records[id]; r != nil && r.mapped {
		c.lineage.focusOrder++
		r.focused = c.lineage.focusOrder
	}
}

// Generic launch wrappers cannot establish application identity. Configuration
// extends the defaults live; direct process ancestry intentionally bypasses it.
func (c *Core) lineageShell(name string) bool {
	switch name {
	case "sh", "bash", "zsh", "fish", "dash", "ksh", "tcsh", "csh", "nu", "elvish", "xonsh", "env", "sudo", "doas", "setsid", "nohup", "timeout", "systemd-run", "flatpak-spawn":
		return true
	}
	return slices.Contains(c.cfg.LineageShells, name)
}
