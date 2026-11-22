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
	edge := []DropAction{DropAuto, DropScroll, DropWorkspace, DropColumn, DropStash, DropFloat, DropNone}
	type field struct {
		k DropZoneKind
		s DropSide
	}
	table := map[field][]DropAction{
		{DropZoneWindow, DropSideCenter}: {DropSwap, DropNone},
		{DropZoneWindow, DropSideLeft}:   {DropColumn, DropSwap, DropNone},
		{DropZoneWindow, DropSideRight}:  {DropColumn, DropSwap, DropNone},
		{DropZoneWindow, DropSideTop}:    {DropStack, DropSwap, DropNone},
		{DropZoneWindow, DropSideBottom}: {DropStack, DropSwap, DropNone},
		{DropZoneGap, DropSideCenter}:    {DropColumn, DropNone},
		{DropZoneEdge, DropSideLeft}:     edge,
		{DropZoneEdge, DropSideRight}:    edge,
		{DropZoneEdge, DropSideTop}:      edge,
		{DropZoneEdge, DropSideBottom}:   edge,
		{DropZoneEdge, DropSideCenter}:   {},
		{DropZoneWindow, DropSide(9)}:    {},
	}
	for f, allowed := range table {
		for _, a := range all {
			want := false
			for _, b := range allowed {
				want = want || a == b
			}
			if got := DropAllowed(f.k, f.s, a); got != want {
				t.Errorf("DropAllowed(%v, %v, %v) = %v, want %v", f.k, f.s, a, got, want)
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

func TestDropPolicyNoAllocs(t *testing.T) {
	var ws, out DropPolicy
	ws.SetWindow(DropSideLeft, DropNone)
	out.SetEdge(DropSideTop, DropStash)
	var sink DropPolicy
	var ok bool
	var label string
	allocs := testing.AllocsPerRun(100, func() {
		sink = ws.Over(out.Over(DefaultDropPolicy()))
		ok = DropAllowed(DropZoneEdge, DropSideTop, sink.Edge[DropSideTop])
		label = sink.Window[DropSideCenter].Label()
	})
	if allocs != 0 || !ok || label != "swap" {
		t.Fatalf("allocs %.1f, ok %v, label %q", allocs, ok, label)
	}
}
