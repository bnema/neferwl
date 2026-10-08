package core

import (
	"cmp"
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// publishState sends the script-facing snapshot when it changed. It is
// built in the owner's scratch every publish and copied only when sent: an
// animation frame that changes nothing allocates nothing here.
func (c *Core) publishState() {
	if c.ch.State == nil {
		return
	}
	c.scratch.state = c.stateInto(c.scratch.state)
	// The first snapshot is always sent, even an empty one.
	if c.sent.state.Outputs != nil && sameState(c.scratch.state, c.sent.state) {
		return
	}
	st := cloneState(c.scratch.state)
	c.sent.state = st
	latest(c.ch.State, st)
}

// state is a fresh snapshot, the caller's to keep.
func (c *Core) state() ports.State { return cloneState(c.stateInto(ports.State{})) }

// stateInto builds the snapshot in st's storage: the result aliases it and
// its Window points into its Windows. The placeholder screen is not an
// output.
func (c *Core) stateInto(st ports.State) ports.State {
	st.Output, st.Window = c.cur().name(), nil
	st.Outputs, st.Windows = st.Outputs[:0], st.Windows[:0]
	if c.scratch.shown == nil {
		c.scratch.shown = map[WindowID]bool{}
	}
	shown := c.scratch.shown
	focus, _ := c.cur().mon.Focused()
	for _, s := range c.screens {
		if s.name() == "" {
			continue
		}
		m := s.mon
		cur := m.Current()
		o := ports.OutputState{Name: s.name(), Count: numbered(m), Workspace: cur.Name, WorkspaceID: cur.ID}
		if i := indexOf(m.Workspaces, cur); i >= 0 {
			o.Active = i + 1
		}
		st.Outputs = append(st.Outputs, o)
		// Visible follows the configures: drawn on the output.
		clear(shown)
		// Read at once, into publish's scratch: the snapshot holds no
		// placement.
		c.realBuf = m.layoutInto(c.realBuf)
		for _, p := range c.realBuf {
			shown[p.ID] = onScreen(p, m.Frame())
		}
		for w := range m.all() {
			n := indexOf(m.Workspaces, w) + 1
			c.scratch.ids = w.appendWindows(c.scratch.ids[:0])
			for _, id := range c.scratch.ids {
				rec := c.windows.lookup(id)
				v := ports.WindowState{ID: id, AppID: rec.client.AppID, PID: rec.client.PID, Output: s.name(), Workspace: n, WorkspaceID: w.ID, WorkspaceName: w.Name, Visible: shown[id], IdleInhibit: rec.idleInhibit, Floating: w.isFloat(id)}
				if i := w.stashIndex(id); i >= 0 {
					v.StashIndex, v.StashCount, v.Hidden = i+1, len(w.Stash), w.stashHidden
				}
				if !v.Floating {
					v.Column, v.Row = w.cell(id)
				}
				st.Windows = append(st.Windows, v)
			}
		}
	}
	slices.SortFunc(st.Windows, func(a, b ports.WindowState) int { return cmp.Compare(a.ID, b.ID) })
	for i := range st.Windows {
		if st.Windows[i].ID == focus && focus != 0 {
			st.Window = &st.Windows[i]
		}
	}
	return st
}

func sameState(a, b ports.State) bool {
	return a.Output == b.Output && slices.Equal(a.Outputs, b.Outputs) && slices.Equal(a.Windows, b.Windows) &&
		(a.Window == nil) == (b.Window == nil) && (a.Window == nil || *a.Window == *b.Window)
}

// cloneState copies st out of the scratch: empty lists stay non-nil (JSON
// []), and Window is its own copy.
func cloneState(st ports.State) ports.State {
	out := ports.State{Output: st.Output, Outputs: append([]ports.OutputState{}, st.Outputs...), Windows: append([]ports.WindowState{}, st.Windows...)}
	if st.Window != nil {
		w := *st.Window
		out.Window = &w
	}
	return out
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
