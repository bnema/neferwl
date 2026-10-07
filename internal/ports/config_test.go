package ports

import (
	"reflect"
	"testing"
)

func TestSplitCommands(t *testing.T) {
	for v, want := range map[string][][]string{
		"":                       nil,
		" ; ;":                   nil,
		"waybar":                 {{"waybar"}},
		"a --x=1,2;b  c ; ;d":    {{"a", "--x=1,2"}, {"b", "c"}, {"d"}},
		"wl-paste --watch c s ;": {{"wl-paste", "--watch", "c", "s"}},
	} {
		if got := SplitCommands(v); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %q, want %q", v, got, want)
		}
	}
}

func TestLayoutRulesOver(t *testing.T) {
	defaults := LayoutRules{MaxColumns: 2, Overflow: "scroll"}
	screen := LayoutRules{MaxColumns: 3}
	ws := LayoutRules{Overflow: "fixed"}
	got := ws.Over(screen.Over(defaults))
	if got != (LayoutRules{MaxColumns: 3, Overflow: "fixed"}) {
		t.Fatal(got)
	}
}
