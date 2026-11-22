package config

import (
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestDropKeys(t *testing.T) {
	c, w := parseString(t, `drop.window.left = none
drop.gap = none
drop.edge.top = stash
drop.DP-1.window.center = none
drop.DP-1.edge.right = auto
drop.HDMI-A.1.edge.left = none
drop.HDMI-A.1.gap = column
workspace.web.drop.window.right = swap
bind.Super+w = workspace web
`)
	if len(w) != 0 {
		t.Fatal(w)
	}
	var global domain.DropPolicy
	global.SetWindow(domain.DropSideLeft, domain.DropNone)
	global.SetGap(domain.DropNone)
	global.SetEdge(domain.DropSideTop, domain.DropStash)
	if c.Drop.DropPolicy != global {
		t.Fatalf("global = %+v", c.Drop.DropPolicy)
	}
	var dp1, hdmi, web domain.DropPolicy
	dp1.SetWindow(domain.DropSideCenter, domain.DropNone)
	dp1.SetEdge(domain.DropSideRight, domain.DropAuto)
	hdmi.SetEdge(domain.DropSideLeft, domain.DropNone)
	hdmi.SetGap(domain.DropColumn)
	want := []domain.OutputDrop{{Output: "DP-1", DropPolicy: dp1}, {Output: "HDMI-A.1", DropPolicy: hdmi}}
	if len(c.Drop.Outputs) != 2 || c.Drop.Outputs[0] != want[0] || c.Drop.Outputs[1] != want[1] {
		t.Fatalf("outputs = %+v", c.Drop.Outputs)
	}
	web.SetWindow(domain.DropSideRight, domain.DropSwap)
	if len(c.Workspaces) != 1 || c.Workspaces[0].Name != "web" || c.Workspaces[0].Drop != web {
		t.Fatalf("workspaces = %+v", c.Workspaces)
	}
}

func TestDropKeysInvalid(t *testing.T) {
	for _, tc := range []struct{ line, msg string }{
		{"drop.window.center = column", `invalid value "column" (allowed: none, swap, tab)`},
		{"drop.window.top = column", "invalid value"},
		{"drop.gap = swap", "invalid value"},
		{"drop.edge.left = bogus", "invalid value"},
		{"drop.DP-1.edge.left = stack", "invalid value"},
		{"workspace.web.drop.window.left = stack", "invalid value"},
		{"drop.window.middle = swap", "unknown key (window.<side>, gap, edge.<side>)"},
		{"drop.edge.center = none", "unknown key"},
		{"drop.DP-1.side = none", "unknown key"},
		{"drop.window = swap", "unknown key"},
		{"workspace.web.drop.foo = none", "unknown key"},
		{"drop.DP-1.window.edge.left = none", "unknown key"},
		{"drop.DP-1.edge.window.left = none", "unknown key"},
		{"drop.edge.gap = none", "unknown key"},
		{"drop.window.gap = none", "unknown key"},
		{"drop.window.center.gap = none", "unknown key"},
		{"drop.edge.left.gap = none", "unknown key"},
		{"drop.DP-1.edge.top.edge.left = none", "unknown key"},
	} {
		c, w := parseString(t, tc.line+"\n")
		if len(w) != 1 || !strings.Contains(w[0].Msg, tc.msg) {
			t.Errorf("%s: warnings %v, want %q", tc.line, w, tc.msg)
		}
		if c.Drop.DropPolicy != (domain.DropPolicy{}) || len(c.Drop.Outputs) != 0 {
			t.Errorf("%s: drop set: %+v", tc.line, c.Drop)
		}
		for _, ws := range c.Workspaces {
			if ws.Drop != (domain.DropPolicy{}) {
				t.Errorf("%s: workspace drop set", tc.line)
			}
		}
	}
	// An invalid value keeps the earlier one.
	c, w := parseString(t, "drop.window.left = swap\ndrop.window.left = stack\n")
	if len(w) != 1 || c.Drop.Window[domain.DropSideLeft] != domain.DropSwap {
		t.Fatal(c.Drop, w)
	}
}

// Keys of one output, set at different places, build one entry; a key set
// twice keeps the later value with the usual duplicate warning.
func TestDropOutputKeysMerge(t *testing.T) {
	c, w := parseString(t, `drop.DP-1.edge.left = none
drop.gap = none
drop.DP-1.gap = column
drop.DP-1.edge.left = stash
`)
	var want domain.DropPolicy
	want.SetEdge(domain.DropSideLeft, domain.DropStash)
	want.SetGap(domain.DropColumn)
	if len(c.Drop.Outputs) != 1 || c.Drop.Outputs[0] != (domain.OutputDrop{Output: "DP-1", DropPolicy: want}) {
		t.Fatalf("outputs = %+v", c.Drop.Outputs)
	}
	if len(w) != 1 || !strings.Contains(w[0].Msg, "drop.DP-1.edge.left") {
		t.Fatalf("warnings %v", w)
	}
}

func TestDropCenterTab(t *testing.T) {
	c, w := parseString(t, "drop.window.center = tab\ndrop.window.left = tab\n")
	if c.Drop.Window[domain.DropSideCenter] != domain.DropTab || len(w) != 1 || !strings.Contains(w[0].Msg, "invalid value") {
		t.Fatalf("center %v, warnings %v", c.Drop.Window[domain.DropSideCenter], w)
	}
}
