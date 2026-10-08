package core

import (
	"image"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestOutputSettingsDisableAndHeadlessMode(t *testing.T) {
	mode := ports.OutputMode{Width: 1920, Height: 1080, RefreshMilli: 60000}
	state := NewOutputOverrides(ports.Config{}, true)
	state.heads = ports.OutputHeads{Heads: []ports.OutputHead{{Info: ports.OutputInfo{Name: "HEADLESS-1"}, Enabled: true, Current: &mode, Modes: []ports.OutputMode{mode}}}}
	if _, err := state.Apply(ports.OutputApply{Heads: []ports.HeadChange{{Name: "HEADLESS-1", Enabled: false}}}); err == nil {
		t.Fatal("headless disable accepted")
	}
	state.headless = false
	cfg, err := state.Apply(ports.OutputApply{Heads: []ports.HeadChange{{Name: "HEADLESS-1", Enabled: false}}})
	if err != nil || !cfg.Outputs[0].Off {
		t.Fatalf("disable: %+v, %v", cfg, err)
	}
}

// A reload that only changes output scales (a saved scale bind) keeps the
// runtime overrides; any other change drops them.
func TestReloadScaleOnlyKeepsOverrides(t *testing.T) {
	mode := ports.OutputMode{Width: 1920, Height: 1080, RefreshMilli: 60000}
	file := ports.Config{Outputs: []ports.OutputConfig{{Name: "DP-1", Scale: 2}}}
	state := NewOutputOverrides(file, false)
	state.heads = ports.OutputHeads{Heads: []ports.OutputHead{{Info: ports.OutputInfo{Name: "DP-1"}, Enabled: true, Current: &mode, Modes: []ports.OutputMode{mode}}, {Info: ports.OutputInfo{Name: "DP-2"}, Enabled: true, Current: &mode, Modes: []ports.OutputMode{mode}}}}
	if _, err := state.Apply(ports.OutputApply{Heads: []ports.HeadChange{{Name: "DP-1", Enabled: true, Pos: &image.Point{X: 100}}, {Name: "DP-2", Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	scaled := file
	scaled.Outputs = []ports.OutputConfig{{Name: "DP-1", Scale: 1.5}, {Name: "DP-2", Scale: 1.25, ScaleOnly: true, SDRBrightness: ports.DefaultSDRBrightness}}
	cfg := state.Reload(scaled)
	if len(cfg.Outputs) != 2 || cfg.Outputs[0].Pos == nil || cfg.Outputs[0].Scale != 1.5 || cfg.Outputs[1].Scale != 1.25 {
		t.Fatalf("scale-only reload: %+v", cfg.Outputs)
	}
	other := scaled
	other.Layout.Gaps = 4
	if cfg := state.Reload(other); cfg.Outputs[0].Pos != nil {
		t.Fatalf("other reload kept overrides: %+v", cfg.Outputs)
	}
}

// A transform change takes the full reload path; a scale change alone does not.
func TestScaleChangesTransform(t *testing.T) {
	old := ports.Config{Outputs: []ports.OutputConfig{{Name: "DP-1", Scale: 2}}}
	cur := ports.Config{Outputs: []ports.OutputConfig{{Name: "DP-1", Scale: 2, Transform: 1}}}
	if _, only := scaleChanges(old, cur); only {
		t.Fatal("a transform change must take the full reload path")
	}
	cur.Outputs[0].Transform = 0
	cur.Outputs[0].Scale = 1.5
	if _, only := scaleChanges(old, cur); !only {
		t.Fatal("a scale-only change stays a scale change")
	}
}

// A runtime transform lands in the effective configuration and survives a
// reload that only changes file scales.
func TestOutputApplyTransform(t *testing.T) {
	mode := ports.OutputMode{Width: 1920, Height: 1080, RefreshMilli: 60000}
	file := ports.Config{Outputs: []ports.OutputConfig{{Name: "DP-1", Scale: 2}}}
	state := NewOutputOverrides(file, false)
	state.heads = ports.OutputHeads{Heads: []ports.OutputHead{{Info: ports.OutputInfo{Name: "DP-1"}, Enabled: true, Current: &mode, Modes: []ports.OutputMode{mode}}}}
	tr := ports.BufferTransform(3)
	cfg, err := state.Apply(ports.OutputApply{Heads: []ports.HeadChange{{Name: "DP-1", Enabled: true, Transform: &tr}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Outputs) != 1 || cfg.Outputs[0].Transform != 3 {
		t.Fatalf("applied transform: %+v", cfg.Outputs)
	}
	scaled := ports.Config{Outputs: []ports.OutputConfig{{Name: "DP-1", Scale: 1.5}}}
	cfg = state.Reload(scaled)
	if cfg.Outputs[0].Transform != 3 || cfg.Outputs[0].Scale != 1.5 {
		t.Fatalf("scale reload lost the runtime transform: %+v", cfg.Outputs)
	}
	// Without a transform in the change, the current one stays.
	cfg, err = state.Apply(ports.OutputApply{Heads: []ports.HeadChange{{Name: "DP-1", Enabled: true}}})
	if err != nil || cfg.Outputs[0].Transform != 3 {
		t.Fatalf("unset transform changed: %+v, %v", cfg.Outputs, err)
	}
	bad := ports.BufferTransform(8)
	if _, err := state.Apply(ports.OutputApply{Heads: []ports.HeadChange{{Name: "DP-1", Enabled: true, Transform: &bad}}}); err == nil {
		t.Fatal("transform 8 accepted")
	}
}

// A runtime position never replaces a file relation: it only takes
// precedence over it in core, and the relation is followed again once the
// override is dropped.
func TestOutputApplyKeepsFileAnchor(t *testing.T) {
	mode := ports.OutputMode{Width: 1920, Height: 1080, RefreshMilli: 60000}
	anchor := ports.OutputAnchor{Relation: ports.RelationRightOf, To: "DP-1", Offset: 40}
	file := ports.Config{Outputs: []ports.OutputConfig{{Name: "DP-2", Anchor: anchor, ScaleOnly: true}}}
	o := NewOutputOverrides(file, false)
	head := func(name string) ports.OutputHead {
		return ports.OutputHead{Info: ports.OutputInfo{Name: name}, Enabled: true, Current: &mode, Modes: []ports.OutputMode{mode}}
	}
	o.heads = ports.OutputHeads{Heads: []ports.OutputHead{head("DP-1"), head("DP-2")}}
	change := func(name string, x int) ports.HeadChange {
		return ports.HeadChange{Name: name, Enabled: true, Pos: &image.Point{X: x}}
	}
	byName := func(cfg ports.Config, name string) ports.OutputConfig {
		t.Helper()
		var found []ports.OutputConfig
		for _, v := range cfg.Outputs {
			if v.Name == name {
				found = append(found, v)
			}
		}
		if len(found) != 1 {
			t.Fatalf("%s entries: %+v", name, cfg.Outputs)
		}
		return found[0]
	}

	cfg, err := o.Apply(ports.OutputApply{ID: 1, Heads: []ports.HeadChange{change("DP-1", 100), {Name: "DP-2", Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	dp1, dp2 := byName(cfg, "DP-1"), byName(cfg, "DP-2")
	if dp1.Pos == nil || dp1.Pos.X != 100 || dp2.Pos != nil || dp2.Anchor != anchor {
		t.Fatalf("position on the reference only: DP-1 %+v, DP-2 %+v", dp1, dp2)
	}

	cfg, err = o.Apply(ports.OutputApply{ID: 2, Heads: []ports.HeadChange{change("DP-1", 100), change("DP-2", 900)}})
	if err != nil {
		t.Fatal(err)
	}
	dp1, dp2 = byName(cfg, "DP-1"), byName(cfg, "DP-2")
	if dp1.Pos == nil || dp1.Pos.X != 100 || dp2.Pos == nil || dp2.Pos.X != 900 || dp2.Anchor != anchor {
		t.Fatalf("position on both: DP-1 %+v, DP-2 %+v", dp1, dp2)
	}

	// A non scale-only reload drops the overrides: the file relation is back alone.
	file.Keyboard.RepeatRate = 33
	cfg = o.Reload(file)
	if dp2 = byName(cfg, "DP-2"); dp2.Pos != nil || dp2.Anchor != anchor || len(cfg.Outputs) != 1 {
		t.Fatalf("after reload: %+v", cfg.Outputs)
	}
}
