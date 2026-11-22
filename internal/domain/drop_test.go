package domain

import "testing"

func TestDropPolicyOverPerField(t *testing.T) {
	var base DropPolicy
	base.SetWindow(DropSideLeft, DropSwap)
	base.SetGap(DropNone)
	base.SetEdge(DropSideRight, DropStash)
	base.SetEdge(DropSideTop, DropFloat)

	var top DropPolicy
	top.SetWindow(DropSideCenter, DropNone)
	top.SetEdge(DropSideRight, DropAuto) // explicit auto overrides the base stash

	got := top.Over(base)
	want := [5]DropAction{DropNone, DropSwap, DropAuto, DropAuto, DropAuto}
	if got.Window != want {
		t.Fatalf("window = %v, want %v", got.Window, want)
	}
	if got.Gap != DropNone {
		t.Fatalf("gap = %v, want none", got.Gap)
	}
	if got.Edge[DropSideRight] != DropAuto || got.Edge[DropSideTop] != DropFloat {
		t.Fatalf("edge = %v", got.Edge)
	}

	// The merged policy keeps its set bits: an explicit auto still wins one level up.
	full := got.Over(DefaultDropPolicy())
	if full.Window[DropSideRight] != DropColumn || full.Window[DropSideLeft] != DropSwap ||
		full.Window[DropSideCenter] != DropNone || full.Edge[DropSideRight] != DropAuto ||
		full.Edge[DropSideTop] != DropFloat || full.Gap != DropNone {
		t.Fatalf("chained = %+v", full)
	}
	var unset DropPolicy
	if unset.Over(base) != base {
		t.Fatal("an empty policy must inherit everything")
	}
}

func TestDefaultDropPolicy(t *testing.T) {
	p := DefaultDropPolicy()
	if p.Window != [5]DropAction{DropSwap, DropColumn, DropColumn, DropStack, DropStack} ||
		p.Gap != DropColumn || p.Edge != [5]DropAction{} {
		t.Fatalf("defaults = %+v", p)
	}
}

func TestDropAllowed(t *testing.T) {
	all := []DropAction{DropAuto, DropNone, DropSwap, DropColumn, DropStack, DropScroll, DropWorkspace, DropStash, DropFloat}
	table := map[string][]DropAction{
		"window.center": {DropSwap, DropNone},
		"window.left":   {DropColumn, DropSwap, DropNone},
		"window.right":  {DropColumn, DropSwap, DropNone},
		"window.top":    {DropStack, DropSwap, DropNone},
		"window.bottom": {DropStack, DropSwap, DropNone},
		"gap":           {DropColumn, DropNone},
		"edge.left":     {DropAuto, DropScroll, DropWorkspace, DropColumn, DropStash, DropFloat, DropNone},
		"edge.right":    {DropAuto, DropScroll, DropWorkspace, DropColumn, DropStash, DropFloat, DropNone},
		"edge.top":      {DropAuto, DropScroll, DropWorkspace, DropColumn, DropStash, DropFloat, DropNone},
		"edge.bottom":   {DropAuto, DropScroll, DropWorkspace, DropColumn, DropStash, DropFloat, DropNone},
		"edge.center":   {},
		"window":        {},
	}
	for key, allowed := range table {
		for _, a := range all {
			want := false
			for _, b := range allowed {
				want = want || a == b
			}
			if got := DropAllowed(key, a); got != want {
				t.Errorf("DropAllowed(%q, %v) = %v, want %v", key, a, got, want)
			}
		}
	}
}

func TestDropActionLabelAndParse(t *testing.T) {
	labels := map[DropAction]string{
		DropAuto: "", DropNone: "", DropSwap: "swap", DropColumn: "column", DropStack: "stack",
		DropScroll: "scroll", DropWorkspace: "workspace", DropStash: "stash", DropFloat: "float",
	}
	for a, want := range labels {
		if got := a.Label(); got != want {
			t.Errorf("%v.Label() = %q, want %q", a, got, want)
		}
		if p, ok := ParseDropAction(a.String()); !ok || p != a {
			t.Errorf("ParseDropAction(%q) = %v, %v", a.String(), p, ok)
		}
		if a.Hover() != (a == DropScroll || a == DropWorkspace) {
			t.Errorf("%v.Hover() wrong", a)
		}
	}
	if _, ok := ParseDropAction("bogus"); ok {
		t.Fatal("unknown value parsed")
	}
}

func TestConfigEqualDrop(t *testing.T) {
	a := Config{Workspaces: []WorkspaceConfig{{Name: "w"}}}
	a.Drop.Outputs = []OutputDrop{{Output: "DP-1"}}
	b := Config{Workspaces: []WorkspaceConfig{{Name: "w"}}}
	b.Drop.Outputs = []OutputDrop{{Output: "DP-1"}}
	if !a.Equal(b) {
		t.Fatal("equal drop configs differ")
	}
	b.Drop.Outputs[0].SetEdge(DropSideLeft, DropNone)
	if a.Equal(b) {
		t.Fatal("output drop change ignored")
	}
	b.Drop.Outputs[0] = a.Drop.Outputs[0]
	b.Workspaces[0].Drop.SetWindow(DropSideCenter, DropNone)
	if a.Equal(b) {
		t.Fatal("workspace drop change ignored")
	}
	b.Workspaces[0].Drop = DropPolicy{}
	b.Drop.SetGap(DropColumn)
	if a.Equal(b) {
		t.Fatal("global drop change ignored")
	}
}
