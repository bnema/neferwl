package core

import (
	"cmp"
	"reflect"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// publishState sends the script-facing snapshot when it changed.
func (c *Core) publishState() {
	if c.ch.State == nil {
		return
	}
	st := c.state()
	if reflect.DeepEqual(st, c.sentState) {
		return
	}
	c.sentState = st
	latest(c.ch.State, st)
}

// state builds the snapshot. The placeholder screen is not an output.
func (c *Core) state() ports.State {
	st := ports.State{Output: c.cur().name(), Outputs: []ports.OutputState{}, Windows: []ports.WindowState{}}
	focus, _ := c.cur().mon.Focused()
	for _, s := range c.screens {
		if s.name() == "" {
			continue
		}
		m := s.mon
		cur := m.Current()
		o := ports.OutputState{Name: s.name(), Count: numbered(m), Workspace: cur.Name}
		if i := indexOf(m.Workspaces, cur); i >= 0 {
			o.Active = i + 1
		}
		st.Outputs = append(st.Outputs, o)
		// Visible follows the configures: drawn on the output.
		shown := map[WindowID]bool{}
		for _, p := range m.Layout() {
			shown[p.ID] = onScreen(p, m.Output())
		}
		for _, w := range m.all() {
			n := indexOf(m.Workspaces, w) + 1
			for _, id := range w.windows() {
				rec := c.windows.lookup(id)
				v := ports.WindowState{ID: id, AppID: rec.client.AppID, PID: rec.client.PID, Output: s.name(), Workspace: n, Visible: shown[id], IdleInhibit: rec.idleInhibit, Floating: w.isFloat(id)}
				if i := w.stashIndex(id); i >= 0 {
					v.StashIndex, v.StashCount, v.Hidden = i+1, len(w.Stash), w.stashHidden
				}
				st.Windows = append(st.Windows, v)
			}
		}
	}
	slices.SortFunc(st.Windows, func(a, b ports.WindowState) int { return cmp.Compare(a.ID, b.ID) })
	for i := range st.Windows {
		if st.Windows[i].ID == focus && focus != 0 {
			w := st.Windows[i]
			st.Window = &w
		}
	}
	return st
}

// numbered counts numbered workspaces, without the trailing spare one
// unless it is on screen.
func numbered(m *Monitor) int {
	n := len(m.Workspaces)
	if last := m.Workspaces[n-1]; n > 1 && last.empty() && last.Name == "" && m.Current() != last {
		n--
	}
	return n
}
