package core

// Monitor owns an ordered list of workspaces for one output (ADR 011). Numbers
// are positions: Cmd+N targets Workspaces[N-1]. An empty workspace always sits
// below the last one, and an empty unnamed workspace is removed once left,
// except the first. Output-wide settings apply to every workspace.
//
// Named workspaces come from config and are never removed while configured.
// A hidden one is not numbered and not reachable by up/down: only its
// `workspace <name>` bind shows it.
type Monitor struct {
	Workspaces []*Workspace
	Active     int
	hidden     []*Workspace
	shown      *Workspace // hidden workspace on screen, nil for Workspaces[Active]
	back       *Workspace // where a named workspace toggle returns to
	template   Workspace
	named      []NamedWorkspace
}

// NamedWorkspace configures a named workspace. Zero MaxColumns and an empty
// Overflow follow the monitor defaults.
type NamedWorkspace struct {
	Name       string
	Hidden     bool
	MaxColumns int
	Overflow   Overflow
}

func NewMonitor() *Monitor {
	m := &Monitor{}
	m.normalize()
	return m
}

// Current is the workspace on screen.
func (m *Monitor) Current() *Workspace {
	if m.shown != nil {
		return m.shown
	}
	return m.Workspaces[m.Active]
}

func (m *Monitor) newWorkspace() *Workspace {
	w := m.template
	w.presets = append([]Width(nil), m.template.presets...)
	return &w
}

// normalize keeps one trailing empty unnamed workspace and drops other empty
// unnamed ones that are neither the first nor active. The active workspace
// never changes.
func (m *Monitor) normalize() {
	spare := func(i int) bool { return m.Workspaces[i].empty() && m.Workspaces[i].Name == "" }
	for i := len(m.Workspaces) - 2; i >= 1; i-- {
		if spare(i) && i != m.Active {
			m.Workspaces = append(m.Workspaces[:i], m.Workspaces[i+1:]...)
			if i < m.Active {
				m.Active--
			}
		}
	}
	for n := len(m.Workspaces); n >= 2 && spare(n-1) && spare(n-2) && m.Active != n-1; n-- {
		m.Workspaces = m.Workspaces[:n-1]
	}
	if n := len(m.Workspaces); n == 0 || !spare(n-1) {
		m.Workspaces = append(m.Workspaces, m.newWorkspace())
	}
}

// Focus switches to the numbered workspace at index i, clamped to the list.
func (m *Monitor) Focus(i int) {
	i = min(max(i, 0), len(m.Workspaces)-1)
	m.shown = nil
	m.Active = i
	m.normalize()
}

// FocusNumber switches to workspace n (1-based); past the end means the last.
func (m *Monitor) FocusNumber(n int) { m.Focus(n - 1) }

// show puts w on screen, numbered or hidden.
func (m *Monitor) show(w *Workspace) {
	if i := indexOf(m.Workspaces, w); i >= 0 {
		m.Focus(i)
		return
	}
	m.shown = w
	m.normalize()
}

// ToggleNamed shows the named workspace, or returns to the previous one when
// it is already on screen. Unknown names do nothing.
func (m *Monitor) ToggleNamed(name string) {
	target := m.byName(name)
	switch cur := m.Current(); {
	case target == nil:
	case target != cur:
		m.back = cur
		m.show(target)
	case m.back != nil && m.has(m.back):
		back := m.back
		m.back = nil
		m.show(back)
	case m.shown != nil:
		m.back = nil
		m.shown = nil
		m.normalize()
	}
}

// byName returns the configured workspace with that name, or nil.
func (m *Monitor) byName(name string) *Workspace {
	for _, w := range append(append([]*Workspace(nil), m.Workspaces...), m.hidden...) {
		if w.Name == name {
			return w
		}
	}
	return nil
}

func indexOf(list []*Workspace, w *Workspace) int {
	for i, v := range list {
		if v == w {
			return i
		}
	}
	return -1
}

// has reports whether w still belongs to the monitor.
func (m *Monitor) has(w *Workspace) bool {
	return indexOf(m.Workspaces, w) >= 0 || indexOf(m.hidden, w) >= 0
}

// find returns the workspace holding the window; i is -1 for hidden ones.
func (m *Monitor) find(id WindowID) (*Workspace, int) {
	for i, w := range m.Workspaces {
		if w.has(id) {
			return w, i
		}
	}
	for _, w := range m.hidden {
		if w.has(id) {
			return w, -1
		}
	}
	return nil, -1
}

// AddWindow places a new window on the workspace on screen.
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

// SetFullscreen applies a client request on the window's own workspace. It does
// not switch workspaces: client requests never move the user.
func (m *Monitor) SetFullscreen(id WindowID, on bool) {
	if w, _ := m.find(id); w != nil {
		w.SetFullscreen(id, on)
	}
}

// MoveToWorkspace moves the focused window to numbered workspace index i;
// focus stays here.
func (m *Monitor) MoveToWorkspace(i int) {
	i = min(max(i, 0), len(m.Workspaces)-1)
	cur := m.Current()
	id, ok := cur.Focused()
	if !ok || m.Workspaces[i] == cur {
		return
	}
	cur.RemoveWindow(id)
	m.Workspaces[i].AddWindow(id)
	m.normalize()
}

func (m *Monitor) Focused() (WindowID, bool) { return m.Current().Focused() }

// Layout places every window: the workspace on screen laid out, others hidden.
func (m *Monitor) Layout() []Placement {
	var result []Placement
	cur := m.Current()
	for _, w := range append(append([]*Workspace(nil), m.Workspaces...), m.hidden...) {
		if w == cur {
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
	for _, w := range m.hidden {
		f(w)
	}
}
func (m *Monitor) SetOutput(width, height int) {
	m.each(func(w *Workspace) { w.SetOutput(width, height) })
}
func (m *Monitor) SetUsable(r Rect)     { m.each(func(w *Workspace) { w.SetUsable(r) }) }
func (m *Monitor) SetGaps(g int)        { m.each(func(w *Workspace) { w.SetGaps(g) }) }
func (m *Monitor) SetPresets(v []Width) { m.each(func(w *Workspace) { w.SetPresets(v) }) }

// SetMaxColumns sets the default; named workspaces may override it.
func (m *Monitor) SetMaxColumns(n int) {
	m.template.MaxColumns = n
	m.applyNamed()
}

// SetOverflow sets the default; named workspaces may override it.
func (m *Monitor) SetOverflow(o Overflow) {
	m.template.Overflow = o
	m.applyNamed()
}

// applyNamed gives every workspace the defaults or its named overrides.
func (m *Monitor) applyNamed() {
	specs := map[string]NamedWorkspace{}
	for _, s := range m.named {
		specs[s.Name] = s
	}
	m.each(func(w *Workspace) {
		s := specs[w.Name]
		w.Overflow = m.template.Overflow
		if s.Overflow != "" {
			w.Overflow = s.Overflow
		}
		w.SetMaxColumns(m.template.MaxColumns)
		if s.MaxColumns > 0 {
			w.SetMaxColumns(s.MaxColumns)
		}
	})
}

// SetNamed applies the named workspaces from config. New numbered ones are
// inserted above the trailing empty workspace (never before the first one).
// A workspace dropped from config becomes a normal one; a dropped hidden one
// hands its windows to the active numbered workspace. The workspace on
// screen stays on screen.
func (m *Monitor) SetNamed(specs []NamedWorkspace) {
	cur := m.Current()
	want := map[string]NamedWorkspace{}
	for _, s := range specs {
		want[s.Name] = s
	}
	byName := map[string]*Workspace{}
	for _, w := range m.Workspaces {
		if _, ok := want[w.Name]; w.Name != "" && !ok {
			w.Name = ""
		}
		if w.Name != "" {
			byName[w.Name] = w
		}
	}
	var hidden []*Workspace
	for _, w := range m.hidden {
		if _, ok := want[w.Name]; ok {
			byName[w.Name] = w
			hidden = append(hidden, w)
			continue
		}
		if w == cur {
			cur = nil
		}
		for _, c := range w.Columns {
			for _, id := range c.Windows {
				m.Workspaces[m.Active].AddWindow(id)
			}
		}
	}
	m.hidden = hidden
	m.named = append([]NamedWorkspace(nil), specs...)
	var nextHidden []*Workspace
	for _, s := range specs {
		w := byName[s.Name]
		if w == nil {
			w = m.newWorkspace()
			w.Name = s.Name
		}
		i := indexOf(m.Workspaces, w)
		switch {
		case s.Hidden:
			if i >= 0 {
				m.removeNumbered(i)
			}
			nextHidden = append(nextHidden, w)
		case i < 0:
			at := max(len(m.Workspaces)-1, 1)
			m.Workspaces = append(m.Workspaces[:at], append([]*Workspace{w}, m.Workspaces[at:]...)...)
			if at <= m.Active {
				m.Active++
			}
		}
	}
	m.hidden = nextHidden
	if m.back != nil && !m.has(m.back) {
		m.back = nil
	}
	m.shown = nil
	switch {
	case cur == nil:
	case indexOf(m.hidden, cur) >= 0:
		m.shown = cur
	case indexOf(m.Workspaces, cur) >= 0:
		m.Active = indexOf(m.Workspaces, cur)
	}
	m.applyNamed()
	m.normalize()
}

// removeNumbered drops Workspaces[i] and keeps Active on the same workspace
// when possible.
func (m *Monitor) removeNumbered(i int) {
	m.Workspaces = append(m.Workspaces[:i], m.Workspaces[i+1:]...)
	if i < m.Active || m.Active >= len(m.Workspaces) {
		m.Active = max(m.Active-1, 0)
	}
	if len(m.Workspaces) == 0 {
		m.Workspaces = append(m.Workspaces, m.newWorkspace())
	}
}
