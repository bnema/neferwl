package core

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"

	"github.com/bnema/neferwl/internal/core/layout"
	workspacedomain "github.com/bnema/neferwl/internal/core/workspace"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// Window rules (rule.<name>.* keys) merge placement and SDR expansion settings.
// Placement applies at the first map in spawnPlacement.place. Title rules may
// also place a window once when a later title first matches; app ID changes
// and config reloads alone move nothing. SDR expansion
// fields are retained for eligibility policy and do not affect placement.
//
// A rule never moves the user (ADR 011): it does not take the focus and does
// not change which workspace or output is shown or focused. A window sent to
// another workspace joins it quietly, like a slot window. The exceptions:
// the workspace on screen of the focused output, where the window behaves
// like any new window, and a window a rule sends to another output (even an
// isolated one) that would have taken the focus on the focused output: it
// takes the focus there, as does a focused window a title rule moves. The
// rule moved the window, not the user's attention.

// rule is one parsed window rule.
type rule struct {
	sdrExpand          *domain.SDRExpandRule
	sdrExpandIntensity *float64
	sdrExpandPeak      *float64
	sdrDecode          *domain.SDRDecode

	appID    *regexp.Regexp
	title    *regexp.Regexp
	key      string
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
		p := rule{key: r.Name, title: r.Title, appID: r.AppID, floating: r.Floating, output: r.Output, sdrExpand: r.SDRExpand, sdrExpandIntensity: r.SDRExpandIntensity, sdrExpandPeak: r.SDRExpandPeak, sdrDecode: r.SDRDecode}
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
	return matchRules(c.rules, v.AppID, v.Title)
}

// matchRules is independent of window lifecycle and placement. It merges every
// app-ID match in config order; ok distinguishes no match from an empty effect.
func matchRules(rules []rule, appID, title string) (eff ruleEffect, ok bool) {
	for i := range rules {
		r := &rules[i]
		if !r.matches(appID, title) {
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
// With byRule (a window rule, not a map position), follow reports a window
// sent to another output that would have taken the focus on the focused
// output's workspace on screen; the caller activates it.
func (c *Core) placeByRule(v ports.WindowMapped, eff ruleEffect, byRule bool) (follow bool) {
	sc, w := c.ruleTarget(eff)
	return c.placeOnWorkspace(v, eff, sc, w, byRule)
}

// placeOnWorkspace shares destination sizing and focus policy for rules and ancestry.
func (c *Core) placeOnWorkspace(v ports.WindowMapped, eff ruleEffect, sc *screen, w *Workspace, byRule bool) (follow bool) {
	float := v.Floating
	if eff.floating != nil {
		float = *eff.floating
	}
	// Keep-focus outputs advance their visible workspace independently of the
	// seat. Hidden workspaces and ordinary remote outputs remain quiet.
	kept := sc != c.cur() && c.keepsFocus(sc)
	quiet := w != sc.mon.Current() || sc != c.cur() && !kept
	if byRule && sc != c.cur() && !kept && !c.cur().mon.ov.open && !c.security.Protected {
		// A new window takes the focus unless a fullscreen window of
		// another app covers the workspace in view (ADR 011).
		cover := c.cur().mon.Current().Cover()
		follow = cover == 0 || sameApp(v, c.windows.lookup(cover).client) || c.launcherOf(v.ID) == cover
	}
	defer func() { follow = follow && w.Has(v.ID) }()
	switch {
	case float:
		if !quiet && c.placeCoverApp(v, sc, w, eff.cells) {
			return follow
		}
		// A window that already floats keeps the size it maps with; a tile
		// floated by a rule becomes a free float, core sizing it.
		w.AddRuleFloat(v.ID, v.Floating, v.Width, v.Height, quiet)
	case quiet:
		w.AddTile(v.ID, eff.cells, true)
	default:
		if !c.placeLaunched(v, sc, w, eff.cells) && !c.placeCoverApp(v, sc, w, eff.cells) {
			sc.mon.addWindow(v.ID, eff.cells)
		}
		return follow
	}
	sc.mon.normalize()
	return follow
}

func (r rule) matches(appID, title string) bool {
	return r.appID.MatchString(appID) && (r.title == nil || r.title.MatchString(title))
}

// rememberTitleRules counts map-time matches as already applied too.
func (c *Core) rememberTitleRules(id WindowID) {
	rec := c.windows.records[id]
	rec.appliedTitleRules = c.titleHistories[id]
	for _, r := range c.rules {
		if r.title != nil && r.matches(rec.client.AppID, rec.client.Title) {
			if rec.appliedTitleRules == nil {
				rec.appliedTitleRules = c.titleHistory(id)
			}
			rec.appliedTitleRules.applied[r.key] = true
		}
	}
}

func (c *Core) windowTitle(ctx context.Context, v ports.WindowTitle) error {
	rec := c.windows.records[v.ID]
	if rec == nil || rec.client.ID == 0 {
		return nil
	}
	old := rec.client.Title
	rec.client.Title = v.Title
	var eff ruleEffect
	apply := false
	for _, r := range c.rules {
		if r.title == nil || rec.appliedTitleRules != nil && rec.appliedTitleRules.applied[r.key] || !r.matches(rec.client.AppID, v.Title) || r.matches(rec.client.AppID, old) {
			continue
		}
		if rec.appliedTitleRules == nil {
			rec.appliedTitleRules = c.titleHistory(v.ID)
		}
		rec.appliedTitleRules.applied[r.key] = true
		if r.output != "" || r.workspace != "" || r.number > 0 {
			eff.merge(r)
			apply = true
		}
	}
	c.matchWindowExpansion(v.ID)
	if !apply {
		return nil
	}
	fromScreen, from := c.screenOf(v.ID)
	if from == nil {
		return nil
	}
	toScreen, to := c.ruleTarget(eff)
	if from == to {
		return nil
	}
	focused, _ := c.cur().mon.Focused()
	// An open overview owns the source output's selection; leave it alone.
	held := focused == v.ID && fromScreen == c.cur() && !fromScreen.mon.ov.open
	workspacedomain.MoveByRule(from, to, v.ID)
	fromScreen.mon.normalize()
	toScreen.mon.normalize()
	// Preserve the existing follow policy except for remote keep-focus
	// outputs: a title change is not a user request to move the seat.
	if held && to.Has(v.ID) && !(toScreen != c.cur() && c.keepsFocus(toScreen)) {
		return c.activate(ctx, v.ID)
	}
	return nil
}

func (c *Core) titleHistory(id WindowID) *titleRuleHistory {
	if c.titleHistories == nil {
		c.titleHistories = map[WindowID]*titleRuleHistory{}
	}
	if c.titleHistories[id] == nil {
		c.titleHistories[id] = &titleRuleHistory{applied: map[string]bool{}}
	}
	return c.titleHistories[id]
}

// keepsFocus reports the output's independent workspace-focus policy.
func (c *Core) keepsFocus(sc *screen) bool {
	for _, o := range c.cfg.Outputs {
		if o.KeepFocus && sc.mon.matches(o.Name) {
			return true
		}
	}
	return false
}
