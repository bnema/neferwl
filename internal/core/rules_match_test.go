package core

import (
	"regexp"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestMatchRules(t *testing.T) {
	on, off := true, false
	app := regexp.MustCompile("^game$")
	rules := []rule{
		{appID: app, floating: &on, number: 2, monitor: "DP-1", cells: 2},
		{appID: regexp.MustCompile("^other$"), monitor: "ignored", cells: 5},
		{appID: app, floating: &off, workspace: "games", cells: 3},
		{appID: app}, // Unset fields must not erase earlier effects.
	}
	want := ruleEffect{floating: &off, workspace: "games", monitor: "DP-1", cells: 3}
	if got, ok := matchRules(rules, "game"); !ok || got != want {
		t.Fatalf("merged effect: %+v, matched=%t; want %+v", got, ok, want)
	}
	// A later numbered workspace replaces a named workspace too.
	rules = append(rules, rule{appID: app, number: 4, monitor: "DP-2"})
	want.number, want.workspace, want.monitor = 4, "", "DP-2"
	if got, ok := matchRules(rules, "game"); !ok || got != want {
		t.Fatalf("numbered replacement: %+v, matched=%t", got, ok)
	}
	if got, ok := matchRules(rules, "unmatched"); ok || got != (ruleEffect{}) {
		t.Fatalf("no match: %+v, matched=%t", got, ok)
	}
	if got, ok := matchRules(nil, "game"); ok || got != (ruleEffect{}) {
		t.Fatalf("no rules: %+v, matched=%t", got, ok)
	}
	if got, ok := matchRules([]rule{{appID: app}}, "game"); !ok || got != (ruleEffect{}) {
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
