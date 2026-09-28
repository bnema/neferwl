package core

import "slices"

// Monitor owns an ordered list of workspaces for one output (ADR 011). Numbers
// are positions: Cmd+N targets Workspaces[N-1]. An empty workspace always sits
// below the last one, and an empty unnamed workspace is removed once left,
// except the first. Output-wide settings apply to every workspace.
//
// Named workspaces come from config and are never removed while configured.
// Each named workspace is not numbered and not reachable by up/down: only its
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
	nextID     *uint64 // shared by the monitors of one Core; owner goroutine only
	named      []NamedWorkspace
	// followMove shows the target workspace after a move to it.
	followMove bool
}

// NamedWorkspace configures a named workspace. Zero MaxColumns and an empty
// Overflow follow the monitor defaults.
type NamedWorkspace struct {
	Name string
	// Monitor is the home monitor (connector or key); "" for the first one.
	Monitor    string
	MaxColumns int
	Overflow   Overflow
}

func NewMonitor() *Monitor { return newMonitor("", "") }

func newMonitor(name, key string) *Monitor {
	return newMonitorWithIDs(name, key, new(uint64))
}

func newMonitorWithIDs(name, key string, nextID *uint64) *Monitor {
	m := &Monitor{Name: name, Key: key, nextID: nextID}
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
	w.stashWidth, w.stashGap = m.template.stashWidth, m.template.stashGap
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
	w.Stash, w.stashAt, w.stashFocus, w.stashHidden, w.hiddenFullscreen = nil, 0, false, false, 0
	(*m.nextID)++
	w.ID = *m.nextID
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

// AddWindow places a new window on the workspace on screen. On a
// fullscreen workspace it floats, hidden until the fullscreen window
// leaves, then tiles after it at home.
func (m *Monitor) AddWindow(id WindowID) {
	if w, _ := m.find(id); w != nil {
		return
	}
	if cur := m.Current(); cur.origin != nil {
		cur.joinFullscreen(id)
		m.normalize()
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
		if w.origin != nil && id == w.back.id && m.has(w.origin) {
			// Its dialogs go home with the view.
			origin, back := w.origin, w.back
			w.RemoveWindow(id)
			w.origin, w.back = nil, origPlace{}
			m.foldInto(w, origin, back.col, false, back.tiled)
		} else {
			w.RemoveWindow(id)
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
	case w.origin != nil && id != w.back.id:
		// A dialog of a fullscreen workspace already floats above it; its
		// own fullscreen would take the workspace from the window it holds.
	case w.stashHidden && w.stashIndex(id) >= 0:
		// A hidden stash stays hidden: its fullscreen waits for the show.
		w.SetFullscreen(id, on)
	case on && m.ownWorkspace(w, id):
		focused, _ := w.Focused()
		m.enterFullscreen(w, id, w == m.Current() && focused == id)
	case !on && w.origin != nil && w.fullscreen == id:
		// The user was on that window: they stay on it, back home.
		m.leaveFullscreen(w, m.Current() == w)
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
	case w.origin != nil && w.fullscreen == w.back.id:
		// From its window or one of its dialogs.
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
	fs.back.id = id
	if f := w.floatIndex(id); f >= 0 {
		fl := w.Floats[f]
		fs.back.float = &fl
		w.RemoveWindow(id)
		fs.AddFloating(id, fl.W, fl.H)
		fs.Floats[len(fs.Floats)-1] = fl
	} else if f := w.stashIndex(id); f >= 0 {
		// It returns to its place in the stash.
		fl := w.Stash[f]
		fs.back.float, fs.back.stash, fs.back.col = &fl, true, f
		w.RemoveWindow(id)
		fs.addStash(fl)
	} else {
		col := slices.IndexFunc(w.Columns, func(c Column) bool { return slices.Contains(c.Windows, id) })
		c := w.Columns[col]
		fs.back = origPlace{id: id, col: col, row: slices.Index(c.Windows, id), slot: c.Slot, width: c.Width, expanded: c.Expanded}
		for _, v := range c.Windows {
			if v != id {
				fs.back.stacked = append(fs.back.stacked, v)
			}
		}
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

// leaveFullscreen sends the window of fs back to its place in the origin,
// with the windows opened there meanwhile (its dialogs) just after it, and
// drops fs; the view goes along when fs was on screen. focus is set when
// the user was on the window: it gets the focus back there.
func (m *Monitor) leaveFullscreen(fs *Workspace, focus bool) {
	if m.fullscreenHome(); fs.origin == nil {
		fs.SetFullscreen(fs.fullscreen, false)
		return
	}
	origin, back, id := fs.origin, fs.back, fs.back.id
	fs.origin, fs.back = nil, origPlace{}
	fs.RemoveWindow(id)
	// A closure: back.col is where the window lands, known after the switch.
	defer func() { m.foldInto(fs, origin, back.col+1, focus, back.tiled) }()
	switch prev := origin.Focus; {
	case back.stash:
		at := min(back.col, len(origin.Stash))
		origin.Stash = slices.Insert(origin.Stash, at, *back.float)
		if focus {
			origin.stashAt = at
			origin.showStash()
		} else if at <= origin.stashAt && len(origin.Stash) > 1 {
			origin.stashAt++
		}
		back.col = len(origin.Columns)
	case back.float != nil && focus:
		origin.AddFloating(id, back.float.W, back.float.H)
		origin.Floats[len(origin.Floats)-1] = *back.float
	case back.float != nil:
		// At the bottom: the top float, maybe focused, stays on top.
		origin.Floats = slices.Insert(origin.Floats, 0, *back.float)
	case slices.ContainsFunc(origin.Columns, back.holdsStack):
		back.col = slices.IndexFunc(origin.Columns, back.holdsStack)
		c := &origin.Columns[back.col]
		// Below the windows that were above it and are still there.
		row := 0
		for _, v := range back.stacked[:back.row] {
			if slices.Contains(c.Windows, v) {
				row++
			}
		}
		row = min(row, len(c.Windows))
		c.Windows = slices.Insert(c.Windows, row, id)
		if focus {
			origin.Focus, c.Focus, origin.floatFocus, origin.stashFocus = back.col, row, false, false
		} else if row <= c.Focus && len(c.Windows) > 1 {
			c.Focus++
		}
	default:
		at := min(back.col, len(origin.Columns))
		// Its column is gone: it comes back expanded, unless another
		// column was expanded meanwhile.
		expanded := back.expanded && !slices.ContainsFunc(origin.Columns, func(c Column) bool { return c.Expanded })
		origin.insertColumn(at, Column{Windows: []WindowID{id}, Width: back.width, Slot: back.slot, Expanded: expanded})
		if focus {
			origin.floatFocus, origin.stashFocus = false, false
		} else if len(origin.Columns) > 1 {
			origin.Focus = prev
			if at <= prev {
				origin.Focus++
			}
		}
	}
}

// foldInto moves what is left in fs (windows opened while fullscreen: its
// dialogs) to origin, tiled ones from column at, floating ones on top, and
// drops fs. The view goes along when fs was on screen.
func (m *Monitor) foldInto(fs, origin *Workspace, at int, focus bool, tiled []WindowID) {
	shown := m.Current() == fs
	at = min(max(at, 0), len(origin.Columns))
	for _, c := range slices.Backward(fs.Columns) {
		origin.Columns = slices.Insert(origin.Columns, at, Column{Windows: c.Windows, Width: c.Width, Focus: c.Focus})
		if at <= origin.Focus && len(origin.Columns) > 1 {
			origin.Focus++
		}
	}
	var floats []Float
	for _, f := range fs.Floats {
		if !slices.Contains(tiled, f.ID) {
			floats = append(floats, f)
			continue
		}
		origin.Columns = slices.Insert(origin.Columns, at, Column{Windows: []WindowID{f.ID}})
		if at <= origin.Focus && len(origin.Columns) > 1 {
			origin.Focus++
		}
		at++
	}
	if focus || shown {
		origin.Floats = append(origin.Floats, floats...)
		origin.floatFocus = origin.floatFocus || len(floats) > 0 && fs.floatFocus
	} else {
		// The user is elsewhere: below, the focused float keeps the focus.
		origin.Floats = append(floats, origin.Floats...)
	}
	// Windows stashed there join the end of the stash.
	for _, f := range fs.Stash {
		origin.Stash = append(origin.Stash, f.rehome())
	}
	fs.Columns, fs.Floats, fs.Stash = nil, nil, nil
	origin.scroll()
	m.take(fs)
	if shown {
		m.show(origin)
	}
}

// awayInSlot reports whether id is away from slot n of w, fullscreen in a
// workspace that returns it there.
func (m *Monitor) awayInSlot(w *Workspace, id WindowID, n int) bool {
	for _, fs := range m.all() {
		if fs.origin == w && fs.back.id == id && fs.back.slot == n {
			return true
		}
	}
	return false
}

// holdsStack reports whether c holds a window of the stack left behind.
func (p origPlace) holdsStack(c Column) bool {
	return slices.ContainsFunc(c.Windows, func(v WindowID) bool { return slices.Contains(p.stacked, v) })
}

// fullscreenHome keeps each fullscreen workspace linked to its origin only
// while its window is still there and fullscreen, and the origin is still
// on this monitor. Otherwise (the window moved out or activated away from
// fullscreen, the origin gone to another monitor) it is a normal workspace.
// Other windows opened there (a dialog) stay when the window returns.
func (m *Monitor) fullscreenHome() {
	for _, w := range m.all() {
		if w.origin == nil {
			continue
		}
		if !m.has(w.origin) || w.fullscreen != w.back.id || !w.has(w.back.id) {
			w.origin, w.back = nil, origPlace{}
		}
	}
}

// MoveToWorkspace moves the focused column (or only the focused window) to
// numbered workspace index i. A floating window always moves alone. Focus
// stays here unless followMove is set (ADR 017).
func (m *Monitor) MoveToWorkspace(i int, column bool) {
	i = min(max(i, 0), len(m.Workspaces)-1)
	cur, to := m.Current(), m.Workspaces[i]
	id, ok := cur.Focused()
	if !ok || to == cur {
		return
	}
	if m.followMove && to.origin != nil {
		// The view follows the window: a fullscreen workspace would hide
		// it, so its window goes home first and the move lands there.
		origin := to.origin
		m.leaveFullscreen(to, false)
		to = origin
		if i = indexOf(m.Workspaces, to); i < 0 || to == cur {
			return
		}
	}
	if f := cur.floatIndex(id); f >= 0 {
		fl := cur.Floats[f]
		cur.RemoveWindow(id)
		to.AddFloating(id, fl.W, fl.H)
		to.Floats[len(to.Floats)-1] = fl
		if to.origin == nil {
			// As receive: an in-place fullscreen would hide it.
			to.Activate(id)
		} else {
			to.FocusID(id)
		}
	} else if f := cur.stashIndex(id); f >= 0 {
		// It joins the end of the stash there, shown and selected.
		fl := cur.Stash[f].rehome()
		cur.RemoveWindow(id)
		to.addStash(fl)
		if to.origin == nil {
			to.Activate(id)
		}
	} else {
		col := Column{Windows: []WindowID{id}}
		ok := true
		if column {
			col, ok = cur.takeColumn()
		} else {
			_, ok = cur.takeWindow()
		}
		if !ok {
			return
		}
		to.receive(col, -1)
	}
	if m.followMove {
		m.Active = i
		m.shown = nil
	}
	m.normalize()
}

// receive adds a column moved in from another workspace and focuses it: at
// index at, or where a new window goes when at < 0. An in-place fullscreen
// is left. On a fixed-overflow fullscreen workspace its windows float,
// hidden until the fullscreen window leaves: it stays exclusive.
func (w *Workspace) receive(col Column, at int) {
	id := col.Windows[col.Focus]
	switch {
	case w.origin != nil:
		for _, v := range col.Windows {
			w.joinFullscreen(v)
		}
		return
	case at < 0:
		w.addColumn(col)
	default:
		w.insertColumn(at, col)
	}
	w.Activate(id)
}

// joinFullscreen adds a tiled window to a fullscreen workspace fs: it
// floats, hidden while the window is fullscreen, and tiles again once home.
func (fs *Workspace) joinFullscreen(id WindowID) {
	fs.AddFloating(id, 0, 0)
	fs.back.tiled = append(fs.back.tiled, id)
}

// landing is the workspace on screen for a window the user moves here. A
// fullscreen workspace hides everything but its window, so it goes home
// first: the moved window must be seen.
func (m *Monitor) landing() *Workspace {
	if fs := m.Current(); fs.origin != nil {
		m.leaveFullscreen(fs, false)
	}
	return m.Current()
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
func (m *Monitor) SetUsable(r Rect) { m.each(func(w *Workspace) { w.SetUsable(r) }) }
func (m *Monitor) SetGaps(g int)    { m.each(func(w *Workspace) { w.SetGaps(g) }) }
func (m *Monitor) SetBorder(b int)  { m.each(func(w *Workspace) { w.border = max(b, 0) }) }

// SetStash sets the width of a stashed window (stash.width; 0 is the
// default) and the space between it and its neighbors (stash.gap), in
// percent of the usable width.
func (m *Monitor) SetStash(width, gap int) {
	if width != 0 {
		width = min(max(width, 10), 90)
	}
	m.each(func(w *Workspace) { w.stashWidth, w.stashGap = width, min(max(gap, 0), 10) })
}
func (m *Monitor) SetPresets(v []Width) { m.each(func(w *Workspace) { w.SetPresets(v) }) }

// SetFollowMove makes moves to another workspace show it (focus.follow-move).
func (m *Monitor) SetFollowMove(on bool) { m.followMove = on }

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

// SetNamed applies bind-only named workspaces from config. Removed named
// workspaces hand their windows to the active numbered workspace.
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
			m.Workspaces[m.Active].Floats[len(m.Workspaces[m.Active].Floats)-1] = f
		}
		for _, f := range w.Stash {
			m.Workspaces[m.Active].Stash = append(m.Workspaces[m.Active].Stash, f.rehome())
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
		if i := indexOf(m.Workspaces, w); i >= 0 {
			m.removeNumbered(i)
		}
		nextHidden = append(nextHidden, w)
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
