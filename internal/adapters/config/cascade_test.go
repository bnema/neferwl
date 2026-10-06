package config

import (
	"strings"
	"testing"
)

func TestCascadeLayoutInheritance(t *testing.T) {
	c, w := parseString(t, "layout.overflow = cascade\nlayout.DP-1.overflow = cascade\nworkspace.work.overflow = cascade\nbind.cmd+w = workspace work\n")
	if len(w) != 0 {
		t.Fatalf("warnings=%v", w)
	}
	if c.Layout.Overflow != "cascade" || c.Layout.Outputs[0].Overflow != "cascade" || c.Workspaces[0].Overflow != "cascade" {
		t.Fatalf("cascade inheritance=%+v", c.Layout)
	}
	_, warnings, err := Parse(strings.NewReader("layout.overflow = unknown\n"))
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0].Msg, "must be scroll, fixed or cascade") {
		t.Fatalf("invalid overflow diagnostic: %v, %v", warnings, err)
	}
}
