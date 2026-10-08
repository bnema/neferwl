package core

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"

	"github.com/bnema/neferwl/internal/ports"
)

// Window rules (rule.<name>.* keys): the app ID of a window that maps for
// the first time picks its place. Rules are evaluated once, at the first
// map, in spawnPlacement.place; a later app ID change, a re-map or a config
// reload moves nothing.
//
// A rule never moves the user (ADR 011): it does not take the focus and does
// not change which workspace or monitor is shown or focused. A window sent to
// another workspace joins it quietly, like a slot window. The one exception
// is the workspace on screen of the focused monitor, where the window
// behaves like any new window.

// rule is one parsed window rule.
type rule struct {
	appID    *regexp.Regexp
	floating *bool
	// number is the 1-based numbered workspace; workspace the name of a
	// named one. At most one is set.
	number    int
	workspace string
	monitor   string
	width     Width // zero: automatic
}

// ruleEffect is the merge of every rule matching a window: per field, the
// last rule in config order that sets it wins.
type ruleEffect struct {
	floating  *bool
	number    int
	workspace string
	monitor   string
	width     Width // zero: automatic
}

func parseRules(rs []ports.WindowRule) ([]rule, error) {
	out := make([]rule, 0, len(rs))
	for _, r := range rs {
		if r.AppID == nil {
			return nil, fmt.Errorf("rule %s: no app-id", r.Name)
		}
		p := rule{appID: r.AppID, floating: r.Floating, monitor: r.Monitor}
		if r.Workspace != "" {
			if n, err := strconv.Atoi(r.Workspace); err == nil {
				if n < 1 {
					return nil, fmt.Errorf("rule %s: invalid workspace %q", r.Name, r.Workspace)
				}
				p.number = n
			} else {
				p.workspace = r.Workspace
			}
		}
		if r.Width != "" {
			width, err := ParseWidth(r.Width)
			if err != nil {
				return nil, fmt.Errorf("rule %s: %w", r.Name, err)
			}
			p.width = width
		}
		out = append(out, p)
	}
	return out, nil
}

// ruleFor merges the rules matching appID; ok is false when none does.
func (c *Core) ruleFor(appID string) (eff ruleEffect, ok bool) {
	for i := range c.rules {
		r := &c.rules[i]
		if !r.appID.MatchString(appID) {
			continue
		}
		ok = true
		if r.floating != nil {
			eff.floating = r.floating
		}
		if r.number > 0 || r.workspace != "" {
			eff.number, eff.workspace = r.number, r.workspace
		}
		if r.monitor != "" {
			eff.monitor = r.monitor
		}
		if r.width != (Width{}) {
			eff.width = r.width
		}
	}
	return eff, ok
}

// ruleTarget is the screen and workspace a rule sends a window to. A named
// workspace lives where it lives, whatever the rule's monitor; an unknown one
// is ignored. A monitor that is not connected is ignored too. Without a
// workspace the monitor's workspace on screen is the target, and a number is
// clamped to the workspaces that exist (the last one is the spare empty one),
// so no empty workspace is created.
func (c *Core) ruleTarget(eff ruleEffect) (*screen, *Workspace) {
	if eff.workspace != "" {
		if sc, w := c.byName(eff.workspace); w != nil {
			return sc, w
		}
	}
	sc := c.cur()
	if eff.monitor != "" {
		if i := slices.IndexFunc(c.screens, func(s *screen) bool { return s.mon.matches(eff.monitor) }); i >= 0 {
			sc = c.screens[i]
		}
	}
	if eff.number > 0 {
		return sc, sc.mon.Workspaces[min(eff.number, len(sc.mon.Workspaces))-1]
	}
	return sc, sc.mon.Current()
}

// placeByRule puts a window that maps for the first time where eff says.
func (c *Core) placeByRule(v ports.WindowMapped, eff ruleEffect) {
	float := v.Floating
	if eff.floating != nil {
		float = *eff.floating
	}
	sc, w := c.ruleTarget(eff)
	// The workspace the user looks at takes a new window as usual; any
	// other one gets it without a change of focus or view.
	quiet := sc != c.cur() || w != sc.mon.Current()
	switch {
	case float:
		// A window that already floats keeps the size it maps with; a tile
		// floated by a rule becomes a free float, core sizing it.
		w.addFloat(w.ruleFloat(v), quiet)
	case quiet:
		w.addColumnQuiet(Column{Windows: []WindowID{v.ID}, Width: eff.width})
	default:
		sc.mon.addWindow(v.ID, eff.width)
		return
	}
	sc.mon.normalize()
}

// ruleFloat is the float of a window a rule floats.
func (w *Workspace) ruleFloat(v ports.WindowMapped) Float {
	if v.Floating {
		return Float{ID: v.ID, W: v.Width, H: v.Height}
	}
	f := Float{ID: v.ID, free: true, cx: 0.5, cy: 0.5}
	if w.Usable.W > 0 && w.Usable.H > 0 {
		// As makeFree: the size is core's, fixed now. Without an output
		// yet, floatRect's default (half the usable area) applies.
		r := w.floatRect(f)
		f.W, f.H = max(r.W-2*w.border, 1), max(r.H-2*w.border, 1)
	}
	return f
}

// addFloat adds a new float; quiet puts it below the others without the
// float focus, so the focused window stays the same.
func (w *Workspace) addFloat(f Float, quiet bool) {
	if f.ID == 0 || w.has(f.ID) {
		return
	}
	if quiet {
		// A workspace with nothing else focuses it, so it has a focus when shown.
		w.floatFocus = w.floatFocus || w.empty()
		w.Floats = slices.Insert(w.Floats, 0, f)
		return
	}
	w.Floats = append(w.Floats, f)
	w.floatFocus = true
}

// addColumnQuiet adds a column where a new window goes, keeping the focus
// where it is (ADR 011 golden rule).
func (w *Workspace) addColumnQuiet(col Column) {
	at := len(w.Columns)
	if w.policy().equalCells {
		if at > 0 {
			w.unmaximize()
		}
	} else if at > 0 {
		at = w.Focus + 1
	}
	w.Columns = slices.Insert(w.Columns, at, col)
	w.scroll()
}
