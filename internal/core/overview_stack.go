package core

import (
	"slices"
)

// stackFront resolves the provisional item against fresh workspace contents;
// an invalidated selection falls back to the nearest surviving stack index.
func (m *Monitor) stackFront(w *Workspace) StackItem {
	items := w.Stack()
	if len(items) == 0 {
		return StackItem{}
	}
	if m.ov.row == w {
		for _, item := range items {
			if item == m.ov.front || item.Kind == StackColumn && m.ov.front.Kind == StackColumn && w.ColumnOf(item.ID) >= 0 && w.ColumnOf(item.ID) == w.ColumnOf(m.ov.front.ID) {
				return item
			}
		}
		return items[min(max(m.ov.frontAt, 0), len(items)-1)]
	}
	return items[0]
}

// setFront records a provisional card and its index for removal fallback.
func (m *Monitor) setFront(w *Workspace, item StackItem) {
	m.ov.row, m.ov.front = w, item
	m.ov.frontAt = slices.Index(w.Stack(), item)
}

// moveStack moves only within the current row. The caller changes workspace
// when the linear stack has no item in that direction.
func (m *Monitor) moveStack(d int) bool {
	w := m.Current()
	items := w.Stack()
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
	if !w.OverviewMaximized() {
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
	if i := w.ColumnOf(m.ov.rows[m.ov.from].Focus); w == m.ov.from && i >= 0 && i != w.Focus {
		return i
	}
	for i := range w.Columns {
		if i != w.Focus {
			return i
		}
	}
	return -1
}
