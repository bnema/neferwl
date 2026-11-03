package core

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// Window rules (rule.<name>.* keys) merge placement and SDR expansion settings.
// Placement applies once, at the first map, in spawnPlacement.place; a later
// app ID change, a re-map or a config reload moves nothing. SDR expansion
// fields are retained for eligibility policy and do not affect placement.
//
// A rule never moves the user (ADR 011): it does not take the focus and does
// not change which workspace or output is shown or focused. A window sent to
// another workspace joins it quietly, like a slot window. The one exception
// is the workspace on screen of the focused output, where the window
// behaves like any new window.

// rule is one parsed window rule.
type rule struct {
	sdrExpand          *domain.SDRExpandRule
	sdrExpandIntensity *float64
	sdrExpandPeak      *float64
	sdrDecode          *domain.SDRDecode

	appID    *regexp.Regexp
	floating *bool
	// number is the 1-based numbered workspace; workspace the name of a
	// named one. At most one is set.
	number    int
	workspace string
	output    string
	cells     int // zero: automatic
}

// ruleEffect is the merge of every rule matching a window: per field, the
// last rule in config order that sets it wins.
type ruleEffect struct {
	sdrExpand          *domain.SDRExpandRule
	sdrExpandIntensity *float64
	sdrExpandPeak      *float64
	sdrDecode          *domain.SDRDecode

	floating  *bool
	number    int
	workspace string
	output    string
	cells     int // zero: automatic
}

func parseRules(rs []domain.WindowRule) ([]rule, error) {
	rs = append(domain.DefaultWindowRules(), rs...)
	out := make([]rule, 0, len(rs))
	for _, r := range rs {
		if r.AppID == nil {
			return nil, fmt.Errorf("rule %s: no app-id", r.Name)
		}
		p := rule{appID: r.AppID, floating: r.Floating, output: r.Output, sdrExpand: r.SDRExpand, sdrExpandIntensity: r.SDRExpandIntensity, sdrExpandPeak: r.SDRExpandPeak, sdrDecode: r.SDRDecode}
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
		if r.Cells < 0 || r.Cells > layout.MaxCells {
			return nil, fmt.Errorf("rule %s: invalid width %d", r.Name, r.Cells)
		}
		p.cells = r.Cells
		out = append(out, p)
	}
	return out, nil
}

// ruleFor merges the rules matching v's app ID; ok is false when none does.
// A toplevel mapping again after an unmap (a tray app restored) opens as
// any other window: rules only place its first map.
func (c *Core) ruleFor(v ports.WindowMapped) (eff ruleEffect, ok bool) {
	if v.Remap {
		return eff, false
	}
	return matchRules(c.rules, v.AppID)
}

// matchRules is independent of window lifecycle and placement. It merges every
// app-ID match in config order; ok distinguishes no match from an empty effect.
func matchRules(rules []rule, appID string) (eff ruleEffect, ok bool) {
	for i := range rules {
		r := &rules[i]
		if !r.appID.MatchString(appID) {
			continue
		}
		ok = true
		eff.merge(*r)
	}
	return eff, ok
}

// merge applies only fields set by r. Numbered and named workspaces are one
// setting: choosing either replaces the other.
func (eff *ruleEffect) merge(r rule) {
	if r.sdrExpand != nil {
		eff.sdrExpand = r.sdrExpand
	}
	if r.sdrExpandIntensity != nil {
		eff.sdrExpandIntensity = r.sdrExpandIntensity
	}
	if r.sdrExpandPeak != nil {
		eff.sdrExpandPeak = r.sdrExpandPeak
	}
	if r.sdrDecode != nil {
		eff.sdrDecode = r.sdrDecode
	}
	if r.floating != nil {
		eff.floating = r.floating
	}
	if r.number > 0 || r.workspace != "" {
		eff.number, eff.workspace = r.number, r.workspace
	}
	if r.output != "" {
		eff.output = r.output
	}
	if r.cells != 0 {
		eff.cells = r.cells
	}
}

// ruleTarget is the screen and workspace a rule sends a window to. A named
// workspace lives where it lives, whatever the rule's output; an unknown one
// is ignored. An output that is not connected is ignored too. Without a
// workspace the output's workspace on screen is the target, and a number is
// clamped to the workspaces that exist (the last one is the spare empty one),
// so no empty workspace is created.
func (c *Core) ruleTarget(eff ruleEffect) (*screen, *Workspace) {
	if eff.workspace != "" {
		if sc, w := c.byName(eff.workspace); w != nil {
			return sc, w
		}
	}
	sc := c.cur()
	if eff.output != "" {
		if i := slices.IndexFunc(c.screens, func(s *screen) bool { return s.mon.matches(eff.output) }); i >= 0 {
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
		if !quiet && c.placeCoverApp(v, sc, w, eff.cells) {
			return
		}
		// A window that already floats keeps the size it maps with; a tile
		// floated by a rule becomes a free float, core sizing it.
		w.AddRuleFloat(v.ID, v.Floating, v.Width, v.Height, quiet)
	case quiet:
		w.AddTile(v.ID, eff.cells, true)
	default:
		if !c.placeCoverApp(v, sc, w, eff.cells) {
			sc.mon.addWindow(v.ID, eff.cells)
		}
		return
	}
	sc.mon.normalize()
}
