package core

import (
	"regexp"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestMatchRules(t *testing.T) {
	on, off := true, false
	app := regexp.MustCompile("^game$")
	rules := []rule{
		{appID: app, floating: &on, number: 2, output: "DP-1", cells: 2},
		{appID: regexp.MustCompile("^other$"), output: "ignored", cells: 5},
		{appID: app, floating: &off, workspace: "games", cells: 3},
		{appID: app}, // Unset fields must not erase earlier effects.
	}
	want := ruleEffect{floating: &off, workspace: "games", output: "DP-1", cells: 3}
	if got, ok := matchRules(rules, "game", ""); !ok || got != want {
		t.Fatalf("merged effect: %+v, matched=%t; want %+v", got, ok, want)
	}
	// A later numbered workspace replaces a named workspace too.
	rules = append(rules, rule{appID: app, number: 4, output: "DP-2"})
	want.number, want.workspace, want.output = 4, "", "DP-2"
	if got, ok := matchRules(rules, "game", ""); !ok || got != want {
		t.Fatalf("numbered replacement: %+v, matched=%t", got, ok)
	}
	if got, ok := matchRules(rules, "unmatched", ""); ok || got != (ruleEffect{}) {
		t.Fatalf("no match: %+v, matched=%t", got, ok)
	}
	if got, ok := matchRules(nil, "game", ""); ok || got != (ruleEffect{}) {
		t.Fatalf("no rules: %+v, matched=%t", got, ok)
	}
	if got, ok := matchRules([]rule{{appID: app}}, "game", ""); !ok || got != (ruleEffect{}) {
		t.Fatalf("empty matching effect: %+v, matched=%t", got, ok)
	}
	c := &Core{rules: rules}
	if got, ok := c.ruleFor(ports.WindowMapped{AppID: "game"}); !ok || got != want {
		t.Fatalf("first map: %+v, matched=%t", got, ok)
	}
	if got, ok := c.ruleFor(ports.WindowMapped{AppID: "game", Remap: true}); ok || got != (ruleEffect{}) {
		t.Fatalf("remap reapplied rules: %+v, matched=%t", got, ok)
	}
}

func TestExpansionRulesMergeAndDefaults(t *testing.T) {
	off, on := domain.SDRExpandForceOff, domain.SDRExpandOn
	intensity, zero, peak := 0.7, 0.0, 800.0
	decode := domain.SDRDecodeSRGB
	rules, err := parseRules([]domain.WindowRule{
		{Name: "first", AppID: regexp.MustCompile(`^steam_app_\d+$`), SDRExpand: &on, SDRExpandIntensity: &intensity, SDRExpandPeak: &peak, SDRDecode: &decode},
		{Name: "last", AppID: regexp.MustCompile(`^steam_app_\d+$`), SDRExpand: &off, SDRExpandIntensity: &zero},
	})
	if err != nil {
		t.Fatal(err)
	}
	eff, ok := matchRules(rules, "steam_app_123", "")
	if !ok || *eff.sdrExpand != off || *eff.sdrExpandIntensity != 0 || *eff.sdrExpandPeak != peak || *eff.sdrDecode != decode {
		t.Fatalf("effect %+v", eff)
	}
	defaults, err := parseRules(nil)
	if err != nil {
		t.Fatal(err)
	}
	eff, ok = matchRules(defaults, "steam_app_123", "")
	if !ok || *eff.sdrExpand != domain.SDRExpandGame {
		t.Fatal("Steam default missing")
	}
	if _, ok = matchRules(defaults, "steam_app_123_launcher", ""); ok {
		t.Fatal("Steam pattern not anchored")
	}
}

func TestTitleMatchingAndExpansion(t *testing.T) {
	on := domain.SDRExpandOn
	intensity, peak, decode := 0.8, 900.0, domain.SDRDecodeSRGB
	c := &Core{windows: newWindowRegistry(), rules: []rule{{key: "title", appID: regexp.MustCompile("^game$"), title: regexp.MustCompile("(?i)big picture"), sdrExpand: &on, sdrExpandIntensity: &intensity, sdrExpandPeak: &peak, sdrDecode: &decode}}}
	c.windows.at(1).client = ports.WindowMapped{ID: 1, AppID: "game", Title: "launcher"}
	c.matchWindowExpansion(1)
	if c.windows.lookup(1).expansionRule != (ruleEffect{}) {
		t.Fatal("early title match")
	}
	c.windowTitle(ports.WindowTitle{ID: 1, Title: "BIG PICTURE mode"})
	eff := c.windows.lookup(1).expansionRule
	if eff.sdrExpand != &on || eff.sdrExpandIntensity != &intensity || eff.sdrExpandPeak != &peak || eff.sdrDecode != &decode {
		t.Fatal(eff)
	}
	c.windowTitle(ports.WindowTitle{ID: 1, Title: "launcher"})
	if c.windows.lookup(1).expansionRule != (ruleEffect{}) {
		t.Fatal("stale expansion match")
	}
	if _, ok := matchRules(c.rules, "other", "big picture"); ok {
		t.Fatal("title bypassed app-id")
	}
}

func TestMapTimeTitleMatchIsRemembered(t *testing.T) {
	c := &Core{windows: newWindowRegistry(), rules: []rule{{key: "title", appID: regexp.MustCompile("^game$"), title: regexp.MustCompile("ready"), output: "DP-2"}}}
	c.windows.at(1).client = ports.WindowMapped{ID: 1, AppID: "game", Title: "ready"}
	if eff, ok := c.ruleFor(c.windows.lookup(1).client); !ok || eff.output != "DP-2" {
		t.Fatal(eff)
	}
	c.rememberTitleRules(1)
	c.windowTitle(ports.WindowTitle{ID: 1, Title: "loading"})
	c.windowTitle(ports.WindowTitle{ID: 1, Title: "ready"})
	if !c.windows.lookup(1).appliedTitleRules.applied["title"] {
		t.Fatal("map match not remembered")
	}
}
