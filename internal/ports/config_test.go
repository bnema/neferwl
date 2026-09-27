package ports

import "testing"

func TestLayoutRulesOver(t *testing.T) {
	defaults := LayoutRules{MaxColumns: 2, Overflow: "scroll"}
	screen := LayoutRules{MaxColumns: 3}
	ws := LayoutRules{Overflow: "fixed"}
	got := ws.Over(screen.Over(defaults))
	if got != (LayoutRules{MaxColumns: 3, Overflow: "fixed"}) {
		t.Fatal(got)
	}
}
