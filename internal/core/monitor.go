package core

import (
	"iter"
	"slices"
)

// Monitor owns an ordered list of workspaces for one output (ADR 011). Numbers
// are positions: Cmd+N targets Workspaces[N-1]. An empty workspace always sits
// below the last one, and an empty unnamed workspace is removed once left.
// Output-wide settings apply to every workspace.
//
// Named workspaces come from config and are never removed while configured.
// Named workspaces are outside normal numbered up/down navigation. Their
// `workspace <name>` binds and the overview can show them.
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
	// slideBuf and slideShown are slideLayout's scratch (the neighbor's
	// layout and its placements by window), reused and never handed out.
	slideBuf   []Placement
	slideShown map[WindowID]Placement
	nextID     *uint64 // shared by the monitors of one Core; owner goroutine only
	named      []NamedWorkspace
	// followMove shows the target workspace after a move to it.
	followMove bool
	// stashCapture puts new windows in the shown stash (stash.capture).
	stashCapture bool
	// switchView slides the view between numbered workspaces: the view is
	// at Active+switchView.off, in workspaces.
	switchView slide
	// switchAxis is frozen from the source workspace for each transition.
	switchAxis layoutAxis
	// switchList is the numbered list a landing slide measures from (the
	// one its swipe began on); switchView.off is then from the current
	// workspace's place in it.
	switchList []*Workspace
	// ov holds the overview selection and its Escape snapshot.
	ov overviewState
	// overviewOpens counts the overview's openings: a swipe sliding when
	// one happens is dropped (gesture.go).
	overviewOpens int
}

// NamedWorkspace configures a named workspace. Zero MaxColumns and an empty
// Overflow follow the monitor defaults.
type NamedWorkspace struct {
	Name string
	// Monitor is the home monitor (connector or key); "" follows the focus.
	Monitor    string
	MaxColumns int
	Overflow   Overflow
	// Size overrides the logical width and height; a zero side inherits
	// the monitor and the scale is always the monitor's.
	Size [2]int
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

// all yields every workspace, numbered then hidden, without copying.
// Callers must not add or remove workspaces while ranging.
func (m *Monitor) all() iter.Seq[*Workspace] {
	return func(yield func(*Workspace) bool) {
		for _, w := range m.Workspaces {
			if !yield(w) {
				return
			}
		}
		for _, w := range m.hidden {
			if !yield(w) {
				return
			}
		}
	}
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
	w.colBuf, w.rowBuf, w.tileBuf, w.zoomBuf = nil, nil, nil, nil
	w.presets = append([]Width(nil), m.template.presets...)
	w.Columns, w.Floats, w.maximized, w.floatFocus, w.home = nil, nil, nil, false, ""
	w.overviewAfter = nil
	w.Stash, w.stashAt, w.stashFocus, w.stashHidden, w.hiddenFullscreen = nil, 0, false, false, 0
	(*m.nextID)++
	w.ID = *m.nextID
	return &w
}

// normalize keeps one trailing empty unnamed workspace and drops other empty
// unnamed ones that are not active. The active workspace never changes.
func (m *Monitor) normalize() {
	spare := func(i int) bool { return m.Workspaces[i].empty() && m.Workspaces[i].Name == "" }
	for i := len(m.Workspaces) - 2; i >= 0; i-- {
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
	for _, w := range m.hidden {
		if indexOf(m.Workspaces, w.overviewAfter) < 0 {
			w.overviewAfter = nil
		}
	}
}

// Focus switches to the numbered workspace at index i, clamped to the list.
func (m *Monitor) Focus(i int) {
	i = min(max(i, 0), len(m.Workspaces)-1)
	m.stopSwitch()
	m.shown = nil
	m.Active = i
	m.normalize()
}

// MoveWorkspace swaps the active numbered workspace with its neighbor up
// (dir -1) or down (dir 1); the view follows it. It never passes the
// trailing empty workspace.
func (m *Monitor) MoveWorkspace(dir int) {
	to := m.Active + dir
	if m.shown != nil || (dir != -1 && dir != 1) || to < 0 || to >= len(m.Workspaces)-1 || m.Active >= len(m.Workspaces)-1 {
		return
	}
	m.Workspaces[m.Active], m.Workspaces[to] = m.Workspaces[to], m.Workspaces[m.Active]
	m.stopSwitch()
	m.Active = to
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
	m.stopSwitch()
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
		anchor := cur
		if m.isHidden(cur) {
			anchor = cur.overviewAfter
		}
		if indexOf(m.Workspaces, anchor) < 0 {
			anchor = m.Workspaces[m.Active]
		}
		target.overviewAfter = anchor
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
	for w := range m.all() {
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

// AddWindow places a new window on the workspace on screen; with
// stash.capture, in its stash when shown.
func (m *Monitor) AddWindow(id WindowID) {
	if w, _ := m.find(id); w != nil {
		return
	}
	if w := m.Current(); !m.stashCapture || !w.captureStash(id) {
		w.AddWindow(id)
	}
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
// workspace.
func (m *Monitor) RemoveWindow(id WindowID) {
	if w, _ := m.find(id); w != nil {
		w.RemoveWindow(id)
		m.normalize()
	}
}

// SetFullscreen applies a client request, in place. A client request never
// moves the user (ADR 011): the view and the focus stay where they are.
func (m *Monitor) SetFullscreen(id WindowID, on bool) {
	if w, _ := m.find(id); w != nil {
		w.SetFullscreen(id, on)
	}
}

// ToggleFullscreen is the bind: the focused window goes fullscreen in place,
// or leaves it. Focus moves leave it too (Workspace.leaveCover).
func (m *Monitor) ToggleFullscreen() { m.Current().ToggleFullscreen() }

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
	if f := cur.floatIndex(id); f >= 0 {
		fl := cur.Floats[f]
		if fl.free && fl.back != nil {
			// Its column place is on this workspace.
			fl = fl.rehome()
		}
		cur.RemoveWindow(id)
		to.AddFloating(id, fl.W, fl.H)
		to.Floats[len(to.Floats)-1] = fl
		// As receive: a fullscreen window would hide it.
		to.Activate(id)
	} else if f := cur.stashIndex(id); f >= 0 {
		// It joins the end of the stash there, shown and selected.
		fl := cur.Stash[f].rehome()
		cur.RemoveWindow(id)
		to.addStash(fl)
		to.Activate(id)
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
// index at, or where a new window goes when at < 0. A fullscreen window
// there is left, so the moved window is seen.
func (w *Workspace) receive(col Column, at int) {
	id := col.Windows[col.Focus]
	switch {
	case at < 0:
		w.addColumn(col)
	default:
		w.insertColumn(at, col)
	}
	w.Activate(id)
}

// Focused is the focused window of the workspace on screen; in the
// overview, the selected stash card when it is one.
func (m *Monitor) Focused() (WindowID, bool) {
	if id := m.card(); id != 0 {
		return id, true
	}
	return m.Current().Focused()
}

// Layout places every window: the workspace on screen laid out, others hidden.
// The slice is fresh, the caller's to keep; the per-frame paths use layoutInto.
func (m *Monitor) Layout() []Placement { return m.layoutInto(nil) }

// layoutInto is Layout built in dst[:0]: the result aliases dst's storage,
// so a reused dst's previous result is overwritten and only a caller done
// with it may pass it. It is never the monitor's own scratch.
func (m *Monitor) layoutInto(dst []Placement) []Placement {
	if m.ov.open {
		return m.overviewLayout(dst[:0])
	}
	result := dst[:0]
	cur := m.Current()
	for w := range m.all() {
		if w == cur {
			result = w.appendLayout(result)
			continue
		}
		result = w.appendHidden(result, true)
	}
	return m.slideLayout(result)
}

// Output is the whole logical monitor, whatever the workspaces' sizes.
func (m *Monitor) Output() Rect { return m.template.Output }

// Frame is where the workspace on screen draws and takes input, in monitor
// coordinates: its viewport, or the whole monitor in the overview. Slides
// involving a sized workspace remain settled inside the current frame.
func (m *Monitor) Frame() Rect {
	if m.ov.open {
		return m.Output()
	}
	return m.Current().Output
}

// frameHas reports whether the output-local point is inside the frame.
func (m *Monitor) frameHas(x, y float64) bool {
	f := m.Frame()
	return x >= float64(f.X) && x < float64(f.X+f.W) && y >= float64(f.Y) && y < float64(f.Y+f.H)
}

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
func (m *Monitor) SetBorder(b int) {
	m.each(func(w *Workspace) { w.border = max(b, 0); w.reconcileFloats() })
}

// SetStash sets the width of a stashed window (stash.width; 0 is the
// default) and the space between it and its neighbors (stash.gap), in
// percent of the usable width.
func (m *Monitor) SetStash(width, gap int) {
	if width != 0 {
		width = min(max(width, 10), 90)
	}
	m.each(func(w *Workspace) { w.stashWidth, w.stashGap = width, min(max(gap, 0), 10) })
}

// SetStashCapture makes new windows join the shown stash (stash.capture).
func (m *Monitor) SetStashCapture(on bool) { m.stashCapture = on }

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
		// Named workspaces take their configured size; any other keeps its own.
		if w.Name != "" {
			w.SetSize(s.Size[0], s.Size[1])
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
			w.SetSize(0, 0)
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
			if f.free && f.back != nil {
				f = f.rehome()
			}
			m.Workspaces[m.Active].AddFloating(f.ID, f.W, f.H)
			m.Workspaces[m.Active].Floats[len(m.Workspaces[m.Active].Floats)-1] = f
		}
		m.Workspaces[m.Active].reconcileFloats()
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
