package core

import (
	"fmt"
	"slices"

	"github.com/bnema/neferwl/internal/domain"
)

// dropPolicy is the drop policy of workspace w on monitor m: its named
// workspace spec, over every drop.<output> entry matching m in config
// order (a later entry wins), over the global drop.* keys, over the
// defaults. It does not allocate.
func (c *Core) dropPolicy(m *Monitor, w *Workspace) domain.DropPolicy {
	p := c.cfg.Drop.Over(domain.DefaultDropPolicy())
	for _, o := range c.cfg.Drop.Outputs {
		if m.matches(o.Output) {
			p = o.Over(p)
		}
	}
	if w.Name == "" {
		return p
	}
	for _, s := range m.named {
		if s.Name == w.Name {
			return s.Drop.Over(p)
		}
	}
	return p
}

// sendDropWarnings sends the drop axis warnings of the config in effect
// that were not sent since the drop config last changed. Like a
// rejection, a full channel drops the warning; it is then not retried.
func (c *Core) sendDropWarnings() {
	for _, msg := range c.dropAxisWarnings(c.cfg) {
		if c.dropWarned[msg] {
			continue
		}
		if c.dropWarned == nil {
			c.dropWarned = map[string]bool{}
		}
		c.dropWarned[msg] = true
		select {
		case c.ch.ConfigErrors <- domain.ConfigWarning{Component: "drag", Msg: msg}:
		default:
		}
	}
}

// sameDropWarnConfig reports whether a and b agree on what the drop axis
// warnings depend on: the drop policies and the layout rules, global, per
// output and per named workspace.
func sameDropWarnConfig(a, b domain.Config) bool {
	if a.Drop.DropPolicy != b.Drop.DropPolicy || !slices.Equal(a.Drop.Outputs, b.Drop.Outputs) ||
		a.Layout.LayoutRules != b.Layout.LayoutRules || !slices.Equal(a.Layout.Outputs, b.Layout.Outputs) ||
		len(a.Workspaces) != len(b.Workspaces) {
		return false
	}
	for i := range a.Workspaces {
		x, y := a.Workspaces[i], b.Workspaces[i]
		if x.Name != y.Name || x.Drop != y.Drop || x.LayoutRules != y.LayoutRules {
			return false
		}
	}
	return true
}

// dropSideNames are the config names of the edges, by DropSide.
var dropSideNames = [...]string{domain.DropSideLeft: "left", domain.DropSideRight: "right", domain.DropSideTop: "top", domain.DropSideBottom: "bottom"}

// dropAxisWarnings lists, once per key, the explicit edge actions of cfg
// that the layout of a screen's current workspace cannot do: workspace
// off the workspace axis, scroll off the content edges of a scrolling
// layout. Those zones are removed. Reload-time only.
func (c *Core) dropAxisWarnings(cfg domain.Config) []string {
	var out []string
	seen := map[string]bool{}
	check := func(key string, p domain.DropPolicy, w *Workspace) {
		for s := domain.DropSideLeft; s <= domain.DropSideBottom; s++ {
			a := p.Edge[s]
			if !p.EdgeSet(s) || a != domain.DropWorkspace && a != domain.DropScroll || w.EdgeAction(s, a) != domain.DropNone {
				continue
			}
			k := key + "edge." + dropSideNames[s]
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, fmt.Sprintf("%s: %s does nothing on this edge in the %s layout (zone removed)", k, a, w.Engine().Name()))
		}
	}
	for _, sc := range c.screens {
		w := sc.mon.Current()
		check("drop.", cfg.Drop.DropPolicy, w)
		for _, o := range cfg.Drop.Outputs {
			if sc.mon.matches(o.Output) {
				check("drop."+o.Output+".", o.DropPolicy, w)
			}
		}
		for _, ws := range cfg.Workspaces {
			if w.Name != "" && ws.Name == w.Name {
				check("workspace."+ws.Name+".drop.", ws.Drop, w)
			}
		}
	}
	return out
}
