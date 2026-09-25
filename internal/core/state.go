package core

import (
	"cmp"
	"reflect"
	"slices"

	"github.com/bnema/nefertty/internal/ports"
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
		for _, w := range m.all() {
			n := indexOf(m.Workspaces, w) + 1
			for _, col := range w.Columns {
				for _, id := range col.Windows {
					info := c.clients[id]
					st.Windows = append(st.Windows, ports.WindowState{ID: id, AppID: info.AppID, PID: info.PID, Output: s.name(), Workspace: n, Visible: w == cur})
				}
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
