package core

import (
	"slices"

	"github.com/bnema/neferwl/internal/ports"
)

// screen is one connected output: its monitor tree, scale, layer surfaces and
// place in the global layout (ADR 011). Outputs are placed left to right, in
// config order (output.<name>.*), then in connection order.
type screen struct {
	info            ports.OutputInfo
	mon             *Monitor
	scale, cfgScale float64
	primary         bool
	x, y            int  // logical origin in the global layout
	off             bool // turned off by a client (output power management)
	layers          []ports.LayerSurface
	placed          []ports.SceneLayer
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

// order sorts screens by config order, then connection order, and assigns x.
func (c *Core) order() {
	rank := func(s *screen) int {
		for i, o := range c.cfg.Outputs {
			if o.Name == s.name() {
				return i
			}
		}
		return len(c.cfg.Outputs)
	}
	focused := c.cur()
	slices.SortStableFunc(c.screens, func(a, b *screen) int { return rank(a) - rank(b) })
	c.focusScreen = slices.Index(c.screens, focused)
	// Explicitly placed outputs reserve their position first; auto-placed
	// outputs follow the rightmost explicit edge in configuration order.
	x := 0
	for _, s := range c.screens {
		for _, o := range c.cfg.Outputs {
			if o.Name == s.name() && o.Pos != nil {
				s.x, s.y = o.Pos.X, o.Pos.Y
				x = max(x, s.x+s.mon.Output().W)
				break
			}
		}
	}
	for _, s := range c.screens {
		explicit := false
		for _, o := range c.cfg.Outputs {
			if o.Name == s.name() && o.Pos != nil {
				explicit = true
				break
			}
		}
		if !explicit {
			s.x, s.y = x, 0
			x += s.mon.Output().W
		}
	}
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
	for _, o := range c.cfg.Outputs {
		if o.Name == name && o.Primary {
			return true
		}
	}
	return false
}

// anyWindow reports whether any workspace holds a window.
func (c *Core) anyWindow() bool {
	for _, s := range c.screens {
		for _, w := range s.mon.all() {
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
	for _, o := range c.cfg.Outputs {
		if o.Name == name && o.Scale != 0 {
			v = o.Scale
		}
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
			for _, w := range host.mon.all() {
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
	m.SetFollowMove(c.cfg.Focus.FollowMove)
}

// neighbor returns the screen index left (-1) or right (+1) of the focused
// one, or -1 at the edge.
func (c *Core) neighbor(dir int) int {
	i := c.focusScreen + dir
	if i < 0 || i >= len(c.screens) {
		return -1
	}
	return i
}

// moveWorkspace moves the workspace on screen to the neighbor screen, where
// it is appended and shown; it becomes its new home (ADR 011).
func (c *Core) moveWorkspace(dir int) {
	to := c.neighbor(dir)
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
