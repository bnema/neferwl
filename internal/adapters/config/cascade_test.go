package config

import (
	"strings"
	"testing"
)

func TestCascadeOverflowInherited(t *testing.T) {
	c, w := parseString(t, "layout.overflow = cascade\nlayout.DP-1.max-columns = 3\nworkspace.work.max-columns = 2\nbind.cmd+w = workspace work\n")
	if len(w) != 0 {
		t.Fatalf("warnings=%v", w)
	}
	out := c.Layout.Outputs[0].LayoutRules.Over(c.Layout.LayoutRules)
	ws := c.Workspaces[0].LayoutRules.Over(out)
	if out.Overflow != "cascade" || ws.Overflow != "cascade" {
		t.Fatalf("output=%q workspace=%q, want cascade", out.Overflow, ws.Overflow)
	}
	_, warnings, err := Parse(strings.NewReader("layout.overflow = unknown\n"))
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0].Msg, "must be scroll, fixed or cascade") {
		t.Fatalf("invalid overflow diagnostic: %v, %v", warnings, err)
	}
}

func TestCascadeIgnoresColumnWidths(t *testing.T) {
	_, w := parseString(t, "workspace.work.overflow = cascade\nworkspace.work.column.1 = 50%, foot\nbind.cmd+w = workspace work\n")
	if len(w) != 1 || !strings.Contains(w[0].Msg, "ignored with overflow = cascade") {
		t.Fatalf("warnings=%v", w)
	}
}
