package core

// Monitor owns an ordered list of workspaces for one output (ADR 011). Numbers
// are positions: Cmd+N targets Workspaces[N-1]. An empty workspace always sits
// below the last one, and an empty workspace is removed once left, except the
// first. Output-wide settings apply to every workspace.
type Monitor struct {
	Workspaces []*Workspace
	Active     int
	template   Workspace
}

func NewMonitor() *Monitor {
	m := &Monitor{}
	m.normalize()
	return m
}

// Current is the workspace on screen.
func (m *Monitor) Current() *Workspace { return m.Workspaces[m.Active] }

func (m *Monitor) newWorkspace() *Workspace {
	w := m.template
	w.presets = append([]Width(nil), m.template.presets...)
	return &w
}

// normalize keeps one trailing empty workspace and drops other empty ones that
// are neither the first nor active. The active workspace never changes.
func (m *Monitor) normalize() {
	for i := len(m.Workspaces) - 2; i >= 1; i-- {
		if m.Workspaces[i].empty() && i != m.Active {
			m.Workspaces = append(m.Workspaces[:i], m.Workspaces[i+1:]...)
			if i < m.Active {
				m.Active--
			}
		}
	}
	for n := len(m.Workspaces); n >= 2 && m.Workspaces[n-1].empty() && m.Workspaces[n-2].empty() && m.Active != n-1; n-- {
		m.Workspaces = m.Workspaces[:n-1]
	}
	if n := len(m.Workspaces); n == 0 || !m.Workspaces[n-1].empty() {
		m.Workspaces = append(m.Workspaces, m.newWorkspace())
	}
}

// Focus switches to the workspace at index i, clamped to the list.
func (m *Monitor) Focus(i int) {
	i = min(max(i, 0), len(m.Workspaces)-1)
	m.Active = i
	m.normalize()
}

// FocusNumber switches to workspace n (1-based); past the end means the last.
func (m *Monitor) FocusNumber(n int) { m.Focus(n - 1) }

// find returns the workspace holding the window.
func (m *Monitor) find(id WindowID) (*Workspace, int) {
	for i, w := range m.Workspaces {
		if w.has(id) {
			return w, i
		}
	}
	return nil, -1
}

// AddWindow places a new window on the active workspace.
func (m *Monitor) AddWindow(id WindowID) {
	if w, _ := m.find(id); w != nil {
		return
	}
	m.Current().AddWindow(id)
	m.normalize()
}

// RemoveWindow drops the window wherever it is; focus stays on the active workspace.
func (m *Monitor) RemoveWindow(id WindowID) {
	if w, _ := m.find(id); w != nil {
		w.RemoveWindow(id)
		m.normalize()
	}
}

// FocusID shows the window's workspace and focuses it.
func (m *Monitor) FocusID(id WindowID) bool {
	w, i := m.find(id)
	if w == nil {
		return false
	}
	m.Active = i
	w.FocusID(id)
	m.normalize()
	return true
}

// SetFullscreen applies a client request on the window's own workspace. It does
// not switch workspaces: client requests never move the user.
func (m *Monitor) SetFullscreen(id WindowID, on bool) {
	if w, _ := m.find(id); w != nil {
		w.SetFullscreen(id, on)
	}
}

// MoveToWorkspace moves the focused window to workspace index i; focus stays here.
func (m *Monitor) MoveToWorkspace(i int) {
	i = min(max(i, 0), len(m.Workspaces)-1)
	cur := m.Current()
	id, ok := cur.Focused()
	if !ok || i == m.Active {
		return
	}
	cur.RemoveWindow(id)
	m.Workspaces[i].AddWindow(id)
	m.normalize()
}

func (m *Monitor) Focused() (WindowID, bool) { return m.Current().Focused() }

// Layout places every window: the active workspace on screen, others hidden.
func (m *Monitor) Layout() []Placement {
	var result []Placement
	for i, w := range m.Workspaces {
		if i == m.Active {
			result = append(result, w.Layout()...)
			continue
		}
		for _, c := range w.Columns {
			for _, id := range c.Windows {
				result = append(result, Placement{ID: id, Hidden: true})
			}
		}
	}
	return result
}

func (m *Monitor) Output() Rect { return m.template.Output }

func (m *Monitor) each(f func(*Workspace)) {
	f(&m.template)
	for _, w := range m.Workspaces {
		f(w)
	}
}
func (m *Monitor) SetOutput(width, height int) {
	m.each(func(w *Workspace) { w.SetOutput(width, height) })
}
func (m *Monitor) SetUsable(r Rect)     { m.each(func(w *Workspace) { w.SetUsable(r) }) }
func (m *Monitor) SetGaps(g int)        { m.each(func(w *Workspace) { w.SetGaps(g) }) }
func (m *Monitor) SetPresets(v []Width) { m.each(func(w *Workspace) { w.SetPresets(v) }) }
func (m *Monitor) SetMaxColumns(n int)  { m.each(func(w *Workspace) { w.SetMaxColumns(n) }) }
