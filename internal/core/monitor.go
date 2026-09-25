package core

import "slices"

// Monitor owns an ordered list of workspaces for one output (ADR 011). Numbers
// are positions: Cmd+N targets Workspaces[N-1]. An empty workspace always sits
// below the last one, and an empty unnamed workspace is removed once left,
// except the first. Output-wide settings apply to every workspace.
//
// Named workspaces come from config and are never removed while configured.
// A hidden one is not numbered and not reachable by up/down: only its
// `workspace <name>` bind shows it.
type Monitor struct {
	// Name is the connector; Key identifies the physical monitor (make,
	// model and serial, else the connector). Workspaces remember the Key of
	// their home monitor.
	Name, Key  string
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
	Name string
	// Monitor is the home monitor (connector or key); "" for the first one.
	Monitor    string
	Hidden     bool
	MaxColumns int
	Overflow   Overflow
}

func NewMonitor() *Monitor { return newMonitor("", "") }

func newMonitor(name, key string) *Monitor {
	m := &Monitor{Name: name, Key: key}
	m.normalize()
	return m
}

// matches reports whether home designates this monitor, by key or connector.
func (m *Monitor) matches(home string) bool {
	return home != "" && (home == m.Key || home == m.Name)
}

// all lists every workspace, numbered then hidden.
func (m *Monitor) all() []*Workspace {
	return append(append([]*Workspace(nil), m.Workspaces...), m.hidden...)
}

// isHidden reports whether w is in the hidden list.
func (m *Monitor) isHidden(w *Workspace) bool { return indexOf(m.hidden, w) >= 0 }

// take removes w from the monitor. The monitor keeps a workspace on screen:
// the one at the same position, or the numbered active one.
func (m *Monitor) take(w *Workspace) {
	if m.back == w {
		m.back = nil
	}
	if i := indexOf(m.hidden, w); i >= 0 {
		m.hidden = append(m.hidden[:i], m.hidden[i+1:]...)
		if m.shown == w {
			m.shown = nil
		}
		m.normalize()
		return
	}
	if i := indexOf(m.Workspaces, w); i >= 0 {
		m.removeNumbered(i)
		m.normalize()
	}
}

// adopt adds a workspace from another monitor: hidden, or numbered at pos
// (clamped above the trailing empty workspace). It takes this monitor's
// output-wide settings and does not change what is on screen.
func (m *Monitor) adopt(w *Workspace, hidden bool, pos int) {
	w.presets = append([]Width(nil), m.template.presets...)
	w.Gaps = m.template.Gaps
	w.border = m.template.border
	w.SetOutput(m.template.Output.W, m.template.Output.H)
	w.SetUsable(m.template.Usable)
	if hidden {
		m.hidden = append(m.hidden, w)
	} else {
		// normalize keeps a trailing empty workspace: stay above it.
		at := min(max(pos, 0), len(m.Workspaces)-1)
		m.Workspaces = slices.Insert(m.Workspaces, at, w)
		if at <= m.Active {
			m.Active++
		}
	}
	m.applyNamed()
	m.normalize()
}

// useSpecs sets the overrides of every named workspace, including those
// that live on other monitors and may move here.
func (m *Monitor) useSpecs(all []NamedWorkspace) {
	m.named = append([]NamedWorkspace(nil), all...)
	m.applyNamed()
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
	w.Columns, w.Floats, w.floatFocus, w.home, w.origin, w.back = nil, nil, false, "", nil, origPlace{}
	return &w
}

// normalize keeps one trailing empty unnamed workspace and drops other empty
// unnamed ones that are neither the first nor active. The active workspace
// never changes.
func (m *Monitor) normalize() {
	m.fullscreenHome()
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

// AddFloating adds a floating window to the active workspace.
func (m *Monitor) AddFloating(id WindowID, width, height int) {
	if w, _ := m.find(id); w != nil {
		return
	}
	m.Current().AddFloating(id, width, height)
	m.normalize()
}

// RemoveWindow drops the window wherever it is; focus stays on the active
// workspace, except that closing a fullscreen workspace goes back to its origin.
func (m *Monitor) RemoveWindow(id WindowID) {
	if w, _ := m.find(id); w != nil {
		w.RemoveWindow(id)
		if w.origin != nil && w.empty() && m.has(w.origin) {
			shown := m.Current() == w
			origin := w.origin
			m.take(w)
			if shown {
				m.show(origin)
			}
		}
		m.normalize()
	}
}

// SetFullscreen applies a client request. Under fixed overflow the window
// gets its own workspace, as with the bind. A client request never moves
// the user (ADR 011): the view follows only a window that is focused and
// on screen, and a window coming back does not take the focus.
func (m *Monitor) SetFullscreen(id WindowID, on bool) {
	w, _ := m.find(id)
	switch {
	case w == nil:
	case on && m.ownWorkspace(w, id):
		focused, _ := w.Focused()
		m.enterFullscreen(w, id, w == m.Current() && focused == id)
	case !on && w.origin != nil && w.fullscreen == id:
		m.leaveFullscreen(w, false)
	default:
		w.SetFullscreen(id, on)
	}
	m.normalize()
}

// ToggleFullscreen is the bind. Under fixed overflow the focused window
// moves to a new workspace below the current one, so workspace up/down
// still reach the others; toggling again sends it back where it was.
func (m *Monitor) ToggleFullscreen() {
	w := m.Current()
	id, ok := w.Focused()
	switch {
	case !ok:
	case w.origin != nil && w.fullscreen == id:
		m.leaveFullscreen(w, true)
	case m.ownWorkspace(w, id):
		m.enterFullscreen(w, id, true)
	default:
		w.ToggleFullscreen()
	}
	m.normalize()
}

// ownWorkspace reports whether going fullscreen moves the window to its own
// workspace: fixed overflow hides every other window behind a fullscreen
// one and never scrolls, so it would leave the user stuck. Not needed when
// the window is alone, or already has its own workspace.
func (m *Monitor) ownWorkspace(w *Workspace, id WindowID) bool {
	return w.Overflow == OverflowFixed && w.origin == nil && w.fullscreen != id && len(w.windows()) > 1 && m.has(w)
}

// enterFullscreen moves window id from w to a new workspace just below the
// numbered one on screen, fullscreen, and shows it when show is set. It
// remembers the window's place in w to return there.
func (m *Monitor) enterFullscreen(w *Workspace, id WindowID, show bool) {
	fs := m.newWorkspace()
	fs.Overflow, fs.MaxColumns = w.Overflow, w.MaxColumns
	fs.origin = w
	if f := w.floatIndex(id); f >= 0 {
		fl := w.Floats[f]
		fs.back.float = &fl
		w.RemoveWindow(id)
		fs.AddFloating(id, fl.W, fl.H)
	} else {
		col := slices.IndexFunc(w.Columns, func(c Column) bool { return slices.Contains(c.Windows, id) })
		c := w.Columns[col]
		fs.back = origPlace{col: col, row: slices.Index(c.Windows, id), slot: c.Slot, stacked: len(c.Windows) > 1, width: c.Width}
		w.RemoveWindow(id)
		fs.AddWindow(id)
		fs.Columns[0].Width = c.Width
	}
	fs.SetFullscreen(id, true)
	at := m.Active + 1
	if i := indexOf(m.Workspaces, w); i >= 0 {
		at = i + 1
	}
	m.Workspaces = slices.Insert(m.Workspaces, at, fs)
	if at <= m.Active {
		m.Active++
	}
	if show {
		m.show(fs)
	}
}

// leaveFullscreen sends the window of fs back to its place in the origin
// and drops fs; the view goes along when fs was on screen. focus is set
// for the user's bind: the window gets the focus back there.
func (m *Monitor) leaveFullscreen(fs *Workspace, focus bool) {
	if m.fullscreenHome(); fs.origin == nil {
		fs.SetFullscreen(fs.fullscreen, false)
		return
	}
	origin, back, id := fs.origin, fs.back, fs.fullscreen
	fs.origin, fs.back = nil, origPlace{}
	fs.RemoveWindow(id)
	switch prev := origin.Focus; {
	case back.float != nil && focus:
		origin.AddFloating(id, back.float.W, back.float.H)
	case back.float != nil:
		// At the bottom: the top float, maybe focused, stays on top.
		origin.Floats = slices.Insert(origin.Floats, 0, Float{ID: id, W: back.float.W, H: back.float.H})
	case back.stacked && back.col < len(origin.Columns):
		c := &origin.Columns[back.col]
		row := min(back.row, len(c.Windows))
		c.Windows = slices.Insert(c.Windows, row, id)
		if focus {
			origin.Focus, c.Focus, origin.floatFocus = back.col, row, false
		} else if row <= c.Focus && len(c.Windows) > 1 {
			c.Focus++
		}
	default:
		at := min(back.col, len(origin.Columns))
		origin.insertColumn(at, Column{Windows: []WindowID{id}, Width: back.width, Slot: back.slot})
		if focus {
			origin.floatFocus = false
		} else if len(origin.Columns) > 1 {
			origin.Focus = prev
			if at <= prev {
				origin.Focus++
			}
		}
	}
	origin.scroll()
	shown := m.Current() == fs
	m.take(fs)
	if shown {
		m.show(origin)
	}
}

// fullscreenHome keeps each fullscreen workspace linked to its origin only
// while it holds just its fullscreen window and the origin is still here.
// Anything else (another window opened or moved in, the window activated
// away from fullscreen, the origin gone to another monitor) makes it a
// normal workspace, so a later exit never moves the wrong window.
func (m *Monitor) fullscreenHome() {
	for _, w := range m.all() {
		if w.origin == nil {
			continue
		}
		if !m.has(w.origin) || w.fullscreen == 0 || len(w.windows()) != 1 || w.windows()[0] != w.fullscreen {
			w.origin, w.back = nil, origPlace{}
		}
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
	if f := cur.floatIndex(id); f >= 0 {
		fl := cur.Floats[f]
		cur.RemoveWindow(id)
		m.Workspaces[i].AddFloating(id, fl.W, fl.H)
	} else {
		cur.RemoveWindow(id)
		m.Workspaces[i].AddWindow(id)
	}
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
		for _, id := range w.windows() {
			result = append(result, Placement{ID: id, Hidden: true})
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
func (m *Monitor) SetBorder(b int)      { m.each(func(w *Workspace) { w.border = max(b, 0) }) }
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
		for _, f := range w.Floats {
			m.Workspaces[m.Active].AddFloating(f.ID, f.W, f.H)
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
