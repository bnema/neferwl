package core

import (
	"slices"

	"github.com/bnema/neferwl/internal/core/layout"
)

// stackFront resolves the provisional item against fresh workspace contents;
// an invalidated selection falls back to the nearest surviving stack index.
func (m *Monitor) stackFront(w *Workspace) stackItem {
	items := w.stack()
	if len(items) == 0 {
		return stackItem{}
	}
	if m.ov.row == w {
		for _, item := range items {
			if item == m.ov.front || item.kind == stackColumn && m.ov.front.kind == stackColumn && w.ColumnOf(item.id) >= 0 && w.ColumnOf(item.id) == w.ColumnOf(m.ov.front.id) {
				return item
			}
		}
		return items[min(max(m.ov.frontAt, 0), len(items)-1)]
	}
	return items[0]
}

// setFront records a provisional card and its index for removal fallback.
func (m *Monitor) setFront(w *Workspace, item stackItem) {
	m.ov.row, m.ov.front = w, item
	m.ov.frontAt = slices.Index(w.stack(), item)
}

// moveStack moves only within the current row. The caller changes workspace
// when the linear stack has no item in that direction.
func (m *Monitor) moveStack(d int) bool {
	w := m.Current()
	items := w.stack()
	if len(items) < 2 {
		return false
	}
	at := slices.Index(items, m.stackFront(w)) + d
	if at < 0 || at >= len(items) {
		return false
	}
	m.setFront(w, items[at])
	m.clearCard()
	return true
}

// hiddenColumn resolves the provisional selection by window, not column
// index. With no surviving selection, prefer the most recently maximized
// hidden column, then the nearest prior non-maximized focus, then first.
func (m *Monitor) hiddenColumn(w *Workspace) int {
	if !w.overviewMaximized() {
		return -1
	}
	if i := w.ColumnOf(m.ov.selected); i >= 0 && i != w.Focus && m.ov.row == w {
		return i
	}
	if m.ov.row == w && m.ov.selectedAt >= 0 {
		i := min(m.ov.selectedAt, len(w.Columns)-1)
		if i == w.Focus {
			if i > 0 {
				i--
			} else {
				i++
			}
		}
		return i
	}
	for _, id := range w.Maximized {
		if i := w.ColumnOf(id); i >= 0 && i != w.Focus {
			return i
		}
	}
	if i := w.ColumnOf(m.ov.rows[m.ov.from].focus); w == m.ov.from && i >= 0 && i != w.Focus {
		return i
	}
	for i := range w.Columns {
		if i != w.Focus {
			return i
		}
	}
	return -1
}

// previewItem draws a group using screen geometry and a single card at its
// configured size. A prior maximized column retains its full-width buffer.
func (m *Monitor) previewItem(w *Workspace, item stackItem, axis layout.Axis, slot Rect, dim, lit bool) []Placement {
	selected := -1
	if item.kind == stackColumns && w.overviewMaximized() {
		selected = m.hiddenColumn(w)
	}
	return w.previewItem(item, selected, axis, slot, dim, lit)
}

// stackRow draws up to two cards on each side of the provisional front,
// fanned along axis.
func (m *Monitor) stackRow(w *Workspace, axis layout.Axis, slot Rect, dim, lit bool) []Placement {
	first, n := w.stackHead()
	if n == 0 {
		return w.previewRow(axis, slot, dim, lit)
	}
	if n == 1 {
		return m.previewItem(w, first, axis, slot, dim, lit)
	}
	front := stackItem{}
	if w == m.Current() {
		front = m.stackFront(w)
	}
	return w.stackRow(front, w == m.Current(), m.hiddenColumn(w), axis, slot, dim, lit)
}
