package config

import (
	"strings"
	"testing"

	"github.com/bnema/kvconf"
)

func TestRulesInFileOrder(t *testing.T) {
	c, w := parseString(t, `
workspace.chat.max-columns = 2
bind.cmd+c = workspace chat
rule.games.app-id = steam_app_.*
rule.games.floating = on
rule.games.workspace = 3
rule.chat.app-id = org\.telegram\..*|discord
rule.chat.workspace = chat
rule.chat.monitor = DP-2
rule.chat.width = 1/3
rule.games.width = 50%
`)
	if len(w) != 0 {
		t.Fatal(w)
	}
	if len(c.Rules) != 2 || c.Rules[0].Name != "games" || c.Rules[1].Name != "chat" {
		t.Fatalf("%+v", c.Rules)
	}
	g, ch := c.Rules[0], c.Rules[1]
	if g.Floating == nil || !*g.Floating || g.Workspace != "3" || g.Width != "50%" || g.Monitor != "" {
		t.Fatalf("%+v", g)
	}
	if ch.Floating != nil || ch.Workspace != "chat" || ch.Monitor != "DP-2" || ch.Width != "1/3" {
		t.Fatalf("%+v", ch)
	}
	if !g.AppID.MatchString("steam_app_1245620") || !ch.AppID.MatchString("discord") || !ch.AppID.MatchString("org.telegram.desktop") {
		t.Fatal("app-id did not match")
	}
}

func TestRuleAppIDIsAnchored(t *testing.T) {
	c, _ := parseString(t, "rule.a.app-id = steam\nrule.b.app-id = steam|foot\n")
	for _, tc := range []struct {
		rule int
		id   string
		want bool
	}{
		{0, "steam", true}, {0, "steam_app_1", false}, {0, "xsteam", false}, {0, "", false},
		// The alternation is anchored as a whole, not per branch.
		{1, "foot", true}, {1, "footclient", false}, {1, "steamfoot", false},
	} {
		if got := c.Rules[tc.rule].AppID.MatchString(tc.id); got != tc.want {
			t.Errorf("rule %d on %q = %v, want %v", tc.rule, tc.id, got, tc.want)
		}
	}
}

func TestRuleWarnings(t *testing.T) {
	for _, tc := range []struct {
		name, text, warn string
		line, rules      int
	}{
		{"bad regex", "rule.a.app-id = (\n", "rule.a.app-id: invalid regular expression", 1, 0},
		// Closing the anchoring group would match any app ID.
		{"anchor breakout", "rule.a.app-id = steam)|(.*\n", "rule.a.app-id: invalid regular expression", 1, 0},
		// Quoting the anchoring group's ")" must warn, not panic.
		{"quoted anchor", "rule.a.app-id = \\Qsteam\n", "rule.a.app-id: invalid regular expression", 1, 0},
		{"missing app-id", "rule.a.floating = on\n", "rule.a: no app-id, rule ignored", 1, 0},
		{"unknown field", "rule.a.app-id = foo\nrule.a.title = x\n", "rule.a.title: unknown key", 2, 1},
		{"bad floating", "rule.a.app-id = foo\nrule.a.floating = maybe\n", "rule.a.floating: must be on or off", 2, 0},
		// An invalid duplicate keeps the earlier value and the rule.
		{"bad duplicate floating", "rule.a.app-id = foo\nrule.a.floating = on\nrule.a.floating = maybe\n", "rule.a.floating: must be on or off", 3, 1},
		{"bad duplicate app-id", "rule.a.app-id = foo\nrule.a.app-id = (\n", "rule.a.app-id: invalid regular expression", 2, 1},
		{"bad width", "rule.a.app-id = foo\nrule.a.width = wide\n", "rule.a.width: must be a width", 2, 0},
		{"bad workspace number", "rule.a.app-id = foo\nrule.a.workspace = 0\n", "rule.a.workspace: workspace number", 2, 0},
		{"bad workspace name", "rule.a.app-id = foo\nrule.a.workspace = a b\n", "rule.a.workspace: must be", 2, 0},
		{"empty monitor", "rule.a.app-id = foo\nrule.a.monitor =\n", "rule.a.monitor: needs", 2, 0},
		{"bad rule name", "rule.a b.app-id = foo\n", "rule name must be", 1, 0},
		{"undeclared workspace", "rule.a.app-id = foo\nrule.a.workspace = web\n", "rule.a.workspace: no workspace.web.* declared", 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, w := parseString(t, tc.text)
			if len(w) != 1 || w[0].Line != tc.line || !strings.Contains(w[0].Msg, tc.warn) {
				t.Fatalf("warnings = %v, want %q at line %d", w, tc.warn, tc.line)
			}
			if len(c.Rules) != tc.rules {
				t.Fatalf("rules = %+v, want %d", c.Rules, tc.rules)
			}
		})
	}
}

func TestRuleWidthReusesColumnWidth(t *testing.T) {
	for _, v := range []string{"1", "33%", "1/3", "640px"} {
		c, w := parseString(t, "rule.a.app-id = foo\nrule.a.width = "+v+"\n")
		if len(w) != 0 || len(c.Rules) != 1 || c.Rules[0].Width != v {
			t.Fatalf("%s: %v %+v", v, w, c.Rules)
		}
	}
}

func TestRuleReloadDiff(t *testing.T) {
	_, a, _ := parseRaw(t, "rule.a.app-id = foo\nrule.b.app-id = bar\n")
	_, b, _ := parseRaw(t, "rule.b.app-id = bar\nrule.a.app-id = foo\n")
	_, c, _ := parseRaw(t, "rule.a.app-id = foo\nrule.b.app-id = bar\nrule.b.floating = on\n")
	if d := kvconf.Changed(a, b); len(d) == 0 {
		t.Fatal("reordering rules is not a change")
	}
	if d := kvconf.Changed(a, c); len(d) != 1 || d[0] != "rule.b.floating" {
		t.Fatal(d)
	}
	if d := kvconf.Changed(a, a); len(d) != 0 {
		t.Fatal(d)
	}
}

func parseRaw(t *testing.T, s string) (cfg any, raw map[string]string, w []Warning) {
	t.Helper()
	c, raw, w, err := parseBytes([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return c, raw, w
}
