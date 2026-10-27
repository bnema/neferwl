package core

import (
	"cmp"
	"slices"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/domain"
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
	// Workspaces keep their Columns storage across calls (see workspaceInto).
	nws := 0
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
		view := m.viewport()
		for _, p := range c.realBuf {
			shown[p.ID] = view.shows(p)
		}
		for w := range m.all() {
			n := indexOf(m.Workspaces, w) + 1
			st.Workspaces = c.workspaceInto(st.Workspaces, nws, w, s.name(), n, w == cur)
			nws++
			c.scratch.ids = w.AppendWindows(c.scratch.ids[:0])
			for _, id := range c.scratch.ids {
				rec := c.windows.lookup(id)
				v := ports.WindowState{ID: id, AppID: rec.client.AppID, PID: rec.client.PID, Sandbox: rec.client.Sandbox, ContentType: contentTypeName(rec.client.ContentType), Xwayland: rec.client.Xwayland, Output: s.name(), Workspace: n, WorkspaceID: w.ID, WorkspaceName: w.Name, Visible: shown[id], IdleInhibit: rec.idleInhibit, ShortcutsInhibited: rec.inhibitShortcuts, Floating: w.IsFloat(id), Fullscreen: w.Fullscreen == id}
				if c.constrained.ID == id {
					switch c.constrained.Mode {
					case domain.ConstraintLock:
						v.PointerConstraint = constraintLocked
					case domain.ConstraintConfine:
						v.PointerConstraint = constraintConfined
					}
				}
				if i := w.StashIndex(id); i >= 0 {
					v.StashIndex, v.StashCount = i+1, len(w.Stash)
					v.Hidden, _ = w.StashVisibility()
				}
				if !v.Floating {
					v.Column, v.Row = w.Cell(id)
				}
				st.Windows = append(st.Windows, v)
			}
		}
	}
	st.Workspaces = st.Workspaces[:nws]
	slices.SortFunc(st.Windows, func(a, b ports.WindowState) int { return cmp.Compare(a.ID, b.ID) })
	for i := range st.Windows {
		if st.Windows[i].ID == focus && focus != 0 {
			st.Window = &st.Windows[i]
		}
	}
	return st
}

// contentTypeName returns the script-facing hint without allocating.
func contentTypeName(kind domain.ContentType) string {
	switch kind {
	case domain.ContentPhoto:
		return contentPhoto
	case domain.ContentVideo:
		return contentVideo
	case domain.ContentGame:
		return contentGame
	default:
		return ""
	}
}

const (
	contentPhoto = "photo"
	contentVideo = "video"
	contentGame  = "game"
)

// Values of ports.WindowState.PointerConstraint.
const (
	constraintLocked   = "locked"
	constraintConfined = "confined"
)

// workspaceInto fills ws[i] (appended when ws is shorter) with w's settled
// geometry, reusing the storage the element held: no slide, fade or zoom,
// floats and the stash left out. A fullscreen tiled window covers the output.
func (c *Core) workspaceInto(ws []ports.WorkspaceState, i int, w *Workspace, output string, number int, visible bool) []ports.WorkspaceState {
	if i < cap(ws) {
		ws = ws[:i+1]
	} else {
		ws = append(ws, ports.WorkspaceState{})
	}
	v := &ws[i]
	cols := v.Columns[:0]
	*v = ports.WorkspaceState{ID: w.ID, Number: number, Name: w.Name, Output: output, Visible: visible, Overflow: string(w.Overflow)}
	c.scratch.rects = w.Engine().ColumnRects(w.Snapshot(), c.scratch.rects, layout.RectOpts{})
	for ci, col := range w.Columns {
		if ci < cap(cols) {
			cols = cols[:ci+1]
		} else {
			cols = append(cols, ports.ColumnState{})
		}
		cv := &cols[ci]
		tiles := cv.Windows[:0]
		r := c.scratch.rects[ci]
		c.scratch.rows = layout.RowRectsInto(c.scratch.rows[:0], r, col, w.Gap())
		for ri, id := range col.Windows {
			tr := c.scratch.rows[ri]
			if w.Fullscreen == id {
				tr = w.Output
			}
			tiles = append(tiles, ports.TileState{ID: id, Rect: tr})
		}
		*cv = ports.ColumnState{Rect: r, Windows: tiles}
	}
	v.Columns = cols
	return ws
}

func sameState(a, b ports.State) bool {
	return a.Output == b.Output && slices.Equal(a.Outputs, b.Outputs) && slices.Equal(a.Windows, b.Windows) &&
		slices.EqualFunc(a.Workspaces, b.Workspaces, func(x, y ports.WorkspaceState) bool {
			return x.ID == y.ID && x.Number == y.Number && x.Name == y.Name && x.Output == y.Output && x.Visible == y.Visible && x.Overflow == y.Overflow &&
				slices.EqualFunc(x.Columns, y.Columns, func(p, q ports.ColumnState) bool {
					return p.Rect == q.Rect && slices.Equal(p.Windows, q.Windows)
				})
		}) &&
		(a.Window == nil) == (b.Window == nil) && (a.Window == nil || *a.Window == *b.Window)
}

// cloneState copies st out of the scratch: empty lists stay non-nil (JSON
// []), and Window is its own copy.
func cloneState(st ports.State) ports.State {
	out := ports.State{Output: st.Output, Outputs: append([]ports.OutputState{}, st.Outputs...), Windows: append([]ports.WindowState{}, st.Windows...)}
	out.Workspaces = make([]ports.WorkspaceState, len(st.Workspaces))
	for i, w := range st.Workspaces {
		out.Workspaces[i] = w
		out.Workspaces[i].Columns = make([]ports.ColumnState, len(w.Columns))
		for j, col := range w.Columns {
			out.Workspaces[i].Columns[j] = ports.ColumnState{Rect: col.Rect, Windows: append([]ports.TileState{}, col.Windows...)}
		}
	}
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
	if last := m.Workspaces[n-1]; n > 1 && last.Empty() && last.Name == "" && m.Current() != last {
		n--
	}
	return n
}
