package core

import (
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// screen is one connected output: its monitor tree, scale, layer surfaces and
// place in the global layout (ADR 011). Outputs are ordered by config
// (output.<name>.*), then by connection; order places them (placeOutputs).
type screen struct {
	info            ports.OutputInfo
	mon             *Monitor
	scale, cfgScale float64
	primary         bool
	x, y            int  // logical origin in the global layout
	off             bool // turned off by a client (output power management)
	layers          []ports.LayerSurface
	placed          []ports.SceneLayer
	// capture holds the attached layers of the capture exclusion: shown over
	// a fullscreen window (capture.go).
	capture map[WindowID]bool
	// capScene and capMarks are what the last scene carried (Capture and
	// CaptureIndicators): immutable, shared by the next one while unchanged.
	capScene *ports.SceneCapture
	capMarks []ports.CaptureIndicator
}

func (s *screen) name() string { return s.info.Name }

func (s *screen) placement() ports.OutputPlacement {
	o := s.mon.Output()
	return ports.OutputPlacement{Info: s.info, X: s.x, Y: s.y, Width: o.W, Height: o.H, Scale: s.scale, Primary: s.primary}
}

// setScale resizes the logical output; layer placement follows.
func (s *screen) setScale(v float64) {
	s.scale = SnapScale(v)
	s.mon.SetOutput(logical(s.info.Width, s.scale), logical(s.info.Height, s.scale))
	s.arrange()
}

// arrange places the layer surfaces and gives workspaces the usable area.
func (s *screen) arrange() {
	var usable ports.Rect
	o := s.mon.Output()
	s.placed, usable = arrangeLayers(o.W, o.H, s.layers)
	s.mon.SetUsable(usable)
}

// order sorts screens by config order, then connection order, and places them
// in the global layout (placeOutputs).
func (c *Core) order() {
	rank := func(s *screen) int {
		if i := c.outputIndex(s.name()); i >= 0 {
			return i
		}
		return len(c.cfg.Outputs)
	}
	focused := c.cur()
	slices.SortStableFunc(c.screens, func(a, b *screen) int { return rank(a) - rank(b) })
	c.focusScreen = slices.Index(c.screens, focused)
	items := make([]placeItem, len(c.screens))
	for i, s := range c.screens {
		o := s.mon.Output()
		items[i].name, items[i].w, items[i].h = s.name(), o.W, o.H
		if cfg, ok := c.outputConfig(s.name()); ok {
			items[i].pos, items[i].anchor = cfg.Pos, cfg.Anchor
		}
	}
	for i, p := range placeOutputs(items) {
		c.screens[i].x, c.screens[i].y = p.X, p.Y
	}
}

// outputIndex is the position of the output.<name> entry in the config, or
// -1; names are unique in the config.
func (c *Core) outputIndex(name string) int {
	return slices.IndexFunc(c.cfg.Outputs, func(o ports.OutputConfig) bool { return o.Name == name })
}

// outputConfig returns the output.<name> entry.
func (c *Core) outputConfig(name string) (ports.OutputConfig, bool) {
	i := c.outputIndex(name)
	if i < 0 {
		return ports.OutputConfig{}, false
	}
	return c.cfg.Outputs[i], true
}

// layout lists the connected outputs; the placeholder is not one.
func (c *Core) layout() ports.Layout {
	l := make(ports.Layout, 0, len(c.screens))
	for _, s := range c.screens {
		if s.name() != "" {
			l = append(l, s.placement())
		}
	}
	return l
}

// cur is the focused screen: new windows open there and binds apply to it.
func (c *Core) cur() *screen { return c.screens[c.focusScreen] }

func (c *Core) screenIndex(name string) int {
	return slices.IndexFunc(c.screens, func(s *screen) bool { return s.name() == name })
}

// screenOf returns the screen and workspace holding the window.
func (c *Core) screenOf(id WindowID) (*screen, *Workspace) {
	for _, s := range c.screens {
		if w, _ := s.mon.find(id); w != nil {
			return s, w
		}
	}
	return nil, nil
}

// byName finds a named workspace on any screen.
func (c *Core) byName(name string) (*screen, *Workspace) {
	for _, s := range c.screens {
		if w := s.mon.byName(name); w != nil {
			return s, w
		}
	}
	return nil, nil
}

// isPrimary reports output.<name>.primary.
func (c *Core) isPrimary(name string) bool {
	o, _ := c.outputConfig(name)
	return o.Primary
}

// anyWindow reports whether any workspace holds a window.
func (c *Core) anyWindow() bool {
	for _, s := range c.screens {
		for w := range s.mon.all() {
			if !w.empty() {
				return true
			}
		}
	}
	return false
}

// configScale is output.<name>.scale, else 1.
func (c *Core) configScale(name string) float64 {
	v := 1.0
	if o, ok := c.outputConfig(name); ok && o.Scale != 0 {
		v = o.Scale
	}
	return SnapScale(v)
}

// applyConfigScales picks up output.<name>.scale when it changed in the
// config, replacing any live scale-up/scale-down adjustment.
func (c *Core) applyConfigScales() {
	for _, s := range c.screens {
		if v := c.configScale(s.name()); v != s.cfgScale {
			s.cfgScale = v
			s.setScale(v)
		}
	}
	c.order()
}

// addScreen handles a connected output or a new mode for a known one. The
// first output takes over the placeholder screen core starts with; a new
// monitor gets its named workspaces and takes its guests back.
func (c *Core) addScreen(info ports.OutputInfo) {
	if i := c.screenIndex(info.Name); i >= 0 {
		s := c.screens[i]
		s.info = info
		if s.mon.Key != info.Key() {
			// Another monitor on the same connector: key rules change.
			s.mon.Key = info.Key()
			c.settings(s.mon)
		}
		s.setScale(s.scale)
		c.order()
		return
	}
	var s *screen
	if len(c.screens) == 1 && c.screens[0].name() == "" {
		s = c.screens[0]
		s.info = info
	} else {
		s = &screen{info: info, mon: newMonitorWithIDs(info.Name, info.Key(), &c.nextWorkspaceID)}
		c.screens = append(c.screens, s)
	}
	s.off = c.offGone[info.Name]
	delete(c.offGone, info.Name)
	s.mon.Name, s.mon.Key = info.Name, info.Key()
	c.settings(s.mon)
	s.cfgScale = c.configScale(info.Name)
	s.setScale(s.cfgScale)
	s.primary = c.isPrimary(info.Name)
	c.order()
	// The primary output takes the focus while nothing is open yet
	// (startup). Later, plugging it in never moves the user.
	if s.primary && !c.anyWindow() {
		c.focusScreen = slices.Index(c.screens, s)
	}
	c.named()
}

// removeScreen moves every workspace of an unplugged output to the focused
// screen (another one if it was focused), numbered ones below its numbered
// workspaces, hidden ones hidden. They remember their home and position and
// return when it is plugged in again. The last output becomes the
// placeholder again: it keeps its windows for the next output.
func (c *Core) removeScreen(name string) {
	i := c.screenIndex(name)
	if i < 0 {
		return
	}
	// A capture session's off-screen workspace of this output goes with it.
	if c.configures.cw.sc == c.screens[i] {
		c.configures.cw.reset()
	}
	if c.screens[i].off {
		if c.offGone == nil {
			c.offGone = map[string]bool{}
		}
		c.offGone[name] = true
	}
	if len(c.screens) == 1 {
		c.screens[0].info = ports.OutputInfo{}
		c.screens[0].layers = nil
		return
	}
	gone := c.screens[i]
	focused := c.cur()
	c.screens = slices.Delete(c.screens, i, i+1)
	c.focusScreen = max(slices.Index(c.screens, focused), 0)
	host := c.cur()
	for pos, w := range gone.mon.Workspaces {
		if w.empty() && w.Name == "" {
			continue
		}
		switch {
		case host.mon.matches(w.home):
			// A guest from the host goes home.
			w.home = ""
			host.mon.adopt(w, false, w.homePos)
			continue
		case w.home == "":
			w.home, w.homePos = gone.info.Key(), pos
		}
		host.mon.adopt(w, false, len(host.mon.Workspaces))
	}
	for _, w := range gone.mon.hidden {
		switch {
		case host.mon.matches(w.home):
			w.home = ""
		case w.home == "":
			w.home = gone.info.Key()
		}
		host.mon.adopt(w, true, 0)
	}
	c.order()
}

// settleGuests returns guests to their connected home monitor, at their
// original positions. A guest on screen stays until the user leaves it
// (golden rule: automatic events never move the user).
func (c *Core) settleGuests() {
	for _, home := range c.screens {
		type guest struct {
			w, anchor *Workspace
			host      *Monitor
			hidden    bool
		}
		var list []guest
		for _, host := range c.screens {
			if host == home {
				continue
			}
			for w := range host.mon.all() {
				if home.mon.matches(w.home) && host.mon.Current() != w {
					list = append(list, guest{w: w, anchor: w.overviewAfter, host: host.mon, hidden: host.mon.isHidden(w)})
				}
			}
		}
		// Snapshot every anchor before take normalizes a host and clears
		// references to numbered workspaces returning alongside named rows.
		for _, g := range list {
			g.host.take(g.w)
		}
		slices.SortStableFunc(list, func(a, b guest) int { return a.w.homePos - b.w.homePos })
		// A monitor showing an empty workspace (just plugged in) shows
		// its first returning workspace instead.
		idle := home.mon.Current().empty() && home.mon.Current().Name == ""
		for _, g := range list {
			g.w.home = ""
			home.mon.adopt(g.w, g.hidden, g.w.homePos)
			if idle && !g.hidden {
				home.mon.show(g.w)
				idle = false
			}
		}
		for _, g := range list {
			if g.hidden && indexOf(home.mon.Workspaces, g.anchor) >= 0 {
				g.w.overviewAfter = g.anchor
			}
		}
	}
}

// named applies the named workspaces from config. One that exists stays on
// its screen; a new one is created on its home monitor, or on the first
// screen as a guest when its home is not connected.
func (c *Core) named() {
	target := map[string]*screen{}
	for _, spec := range c.specs {
		if s, _ := c.byName(spec.Name); s != nil {
			target[spec.Name] = s
			continue
		}
		target[spec.Name] = c.screens[0]
		for _, s := range c.screens {
			if s.mon.matches(spec.Monitor) {
				target[spec.Name] = s
			}
		}
	}
	for _, s := range c.screens {
		var mine []NamedWorkspace
		for _, spec := range c.specs {
			if target[spec.Name] == s {
				mine = append(mine, spec)
			}
		}
		s.mon.SetNamed(mine)
		s.mon.useSpecs(c.specs)
	}
	for _, spec := range c.specs {
		s, w := c.byName(spec.Name)
		w.home = ""
		if spec.Monitor != "" && !s.mon.matches(spec.Monitor) {
			w.home = spec.Monitor
		}
	}
}

// bringNamed prepares a "workspace <name>" toggle from the focused screen.
// The workspace goes to its home monitor when connected, else to the focused
// screen; that screen takes the focus. It reports true when the workspace
// is already on screen there after a focus change: reaching it must not
// toggle it away.
func (c *Core) bringNamed(name string) bool {
	src, w := c.byName(name)
	i := slices.IndexFunc(c.specs, func(s NamedWorkspace) bool { return s.Name == name })
	if w == nil || i < 0 {
		return false
	}
	to, home := c.focusScreen, c.specs[i].Monitor
	if h := slices.IndexFunc(c.screens, func(s *screen) bool { return s.mon.matches(home) }); h >= 0 {
		to, home = h, ""
	}
	dst := c.screens[to]
	if to != c.focusScreen {
		c.focusScreen = to
		if src == dst && dst.mon.Current() == w {
			return true
		}
	}
	if src != dst {
		moveNamed(src, dst, w, home)
	}
	return false
}

// moveNamed moves the named workspace w from src to dst, without changing
// what dst shows. home is its new w.home: a guest keeps waiting for its
// home; any other forgets the output it was unplugged from.
func moveNamed(src, dst *screen, w *Workspace, home string) {
	hidden := src.mon.isHidden(w)
	src.mon.take(w)
	w.home = home
	dst.mon.adopt(w, hidden, len(dst.mon.Workspaces))
}

// settings applies output-wide config to a monitor.
func (c *Core) settings(m *Monitor) {
	rules := c.cfg.Layout.LayoutRules
	for _, o := range c.cfg.Layout.Outputs {
		if m.matches(o.Output) {
			rules = o.Over(rules)
		}
	}
	m.SetOverflow(Overflow(rules.Overflow))
	m.SetMaxColumns(rules.MaxColumns)
	m.SetPresets(c.presets)
	m.SetGaps(c.cfg.Layout.Gaps)
	m.SetBorder(c.cfg.Border.Width)
	m.SetStash(c.cfg.Stash.Width, c.cfg.Stash.Gap)
	m.SetStashCapture(c.cfg.Stash.Capture)
	m.SetFollowMove(c.cfg.Focus.FollowMove)
}

// direction is where a monitor action looks for a neighbor screen.
type direction uint8

const (
	dirLeft direction = iota + 1
	dirRight
	dirUp
	dirDown
)

// neighbor returns the index of the screen next to the focused one in
// direction d, or -1 at the edge. Candidates lie fully beyond the focused
// screen's edge; one sharing part of that edge wins over a diagonal one, then
// the smallest gap, the largest shared edge, the closest centers and the
// lowest index decide. There is no wrap.
func (c *Core) neighbor(d direction) int {
	if d < dirLeft || d > dirDown {
		return -1
	}
	// span is the extent of s along the move (lo, hi) and across it.
	span := func(s *screen) (lo, hi, clo, chi int) {
		o := s.mon.Output()
		if d == dirUp || d == dirDown {
			return s.y, s.y + o.H, s.x, s.x + o.W
		}
		return s.x, s.x + o.W, s.y, s.y + o.H
	}
	lo, hi, clo, chi := span(c.cur())
	best, bestKey := -1, [4]int{}
	for i, s := range c.screens {
		if i == c.focusScreen || s.name() == "" {
			continue
		}
		l, h, cl, ch := span(s)
		gap := l - hi
		if d == dirLeft || d == dirUp {
			gap = lo - h
		}
		if gap < 0 {
			continue
		}
		overlap := min(chi, ch) - max(clo, cl)
		diagonal := 0
		if overlap <= 0 {
			diagonal = 1
		}
		key := [4]int{diagonal, gap, -max(overlap, 0), abs(cl + ch - clo - chi)}
		if best < 0 || slices.Compare(key[:], bestKey[:]) < 0 {
			best, bestKey = i, key
		}
	}
	return best
}

// moveWorkspace moves the workspace on screen to the neighbor screen, where
// it is appended and shown; it becomes its new home (ADR 011).
func (c *Core) moveWorkspace(d direction) {
	to := c.neighbor(d)
	if to < 0 {
		return
	}
	from := c.cur()
	w := from.mon.Current()
	if w.empty() && w.Name == "" {
		return
	}
	hidden := from.mon.isHidden(w)
	from.mon.take(w)
	w.home = ""
	dst := c.screens[to]
	dst.mon.adopt(w, hidden, len(dst.mon.Workspaces))
	dst.mon.show(w)
	c.focusScreen = to
}
