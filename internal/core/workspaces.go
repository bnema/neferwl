package core

import (
	"slices"
	"strconv"

	"github.com/bnema/neferwl/internal/ports"
)

// publishWorkspaces describes the numbered vertical stack and configured hidden
// workspaces. Config-hidden named workspaces and empty numbered spares are
// hidden unless shown; unlike State, hidden named workspaces remain addressable.
// Like publishState, it is built in the owner's scratch and copied only
// when sent.
func (c *Core) publishWorkspaces() {
	if c.ch.Workspaces == nil {
		return
	}
	buf := &c.scratch.workspaces
	buf.Outputs = buf.Outputs[:0]
	for _, sc := range c.screens {
		if sc.name() == "" {
			continue
		}
		m := sc.mon
		// Reuse the list of the entry this one overwrites.
		var list []ports.WorkspaceInfo
		if n := len(buf.Outputs); n < cap(buf.Outputs) {
			list = buf.Outputs[:n+1][n].Workspaces[:0]
		}
		out := ports.WorkspaceOutput{Name: sc.name(), Workspaces: list}
		i := -1
		for w := range m.all() {
			i++
			name := w.Name
			if name == "" {
				name = strconv.Itoa(i + 1)
			}
			out.Workspaces = append(out.Workspaces, ports.WorkspaceInfo{ID: w.ID, Name: name, Index: i, Active: m.Current() == w, Hidden: m.Current() != w && (m.isHidden(w) || w.empty()), Configured: w.Name, Frame: w.Output})
		}
		buf.Outputs = append(buf.Outputs, out)
	}
	// The first snapshot is always sent, even an empty one.
	if c.sent.workspaces.Outputs != nil && sameWorkspaces(*buf, c.sent.workspaces) {
		return
	}
	// Empty lists stay non-nil (JSON []).
	snapshot := ports.Workspaces{Outputs: make([]ports.WorkspaceOutput, len(buf.Outputs))}
	for k, o := range buf.Outputs {
		snapshot.Outputs[k] = ports.WorkspaceOutput{Name: o.Name, Workspaces: append([]ports.WorkspaceInfo{}, o.Workspaces...)}
	}
	c.sent.workspaces = snapshot
	latest(c.ch.Workspaces, snapshot)
}

func sameWorkspaces(a, b ports.Workspaces) bool {
	return slices.EqualFunc(a.Outputs, b.Outputs, func(x, y ports.WorkspaceOutput) bool {
		return x.Name == y.Name && slices.Equal(x.Workspaces, y.Workspaces)
	})
}
