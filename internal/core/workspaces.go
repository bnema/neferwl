package core

import (
	"github.com/bnema/neferwl/internal/ports"
	"reflect"
	"strconv"
)

// publishWorkspaces describes the numbered vertical stack and configured hidden
// workspaces. Empty numbered spares are hidden unless currently shown; unlike
// the script State snapshot, hidden named workspaces remain addressable.
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
		for i, w := range m.all() {
			name := w.Name
			if name == "" {
				name = strconv.Itoa(i + 1)
			}
			out.Workspaces = append(out.Workspaces, ports.WorkspaceInfo{ID: w.ID, Name: name, Index: i, Active: m.Current() == w, Hidden: w.empty() && m.Current() != w})
		}
		snapshot.Outputs = append(snapshot.Outputs, out)
	}
	if reflect.DeepEqual(snapshot, c.sentWorkspaces) {
		return
	}
	c.sentWorkspaces = snapshot
	latest(c.ch.Workspaces, snapshot)
}
