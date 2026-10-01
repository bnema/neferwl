package core

import (
	"reflect"
	"strconv"

	"github.com/bnema/neferwl/internal/ports"
)

// publishWorkspaces describes the numbered vertical stack and configured hidden
// workspaces. Config-hidden named workspaces and empty numbered spares are
// hidden unless shown; unlike State, hidden named workspaces remain addressable.
func (c *Core) publishWorkspaces() {
	if c.ch.Workspaces == nil {
		return
	}
	snapshot := ports.Workspaces{Outputs: []ports.WorkspaceOutput{}}
	for _, sc := range c.screens {
		if sc.name() == "" {
			continue
		}
		m := sc.mon
		out := ports.WorkspaceOutput{Name: sc.name(), Workspaces: []ports.WorkspaceInfo{}}
		i := -1
		for w := range m.all() {
			i++
			name := w.Name
			if name == "" {
				name = strconv.Itoa(i + 1)
			}
			out.Workspaces = append(out.Workspaces, ports.WorkspaceInfo{ID: w.ID, Name: name, Index: i, Active: m.Current() == w, Hidden: m.Current() != w && (m.isHidden(w) || w.empty()), Configured: w.Name, Frame: w.Output})
		}
		snapshot.Outputs = append(snapshot.Outputs, out)
	}
	if reflect.DeepEqual(snapshot, c.sentWorkspaces) {
		return
	}
	c.sentWorkspaces = snapshot
	latest(c.ch.Workspaces, snapshot)
}
