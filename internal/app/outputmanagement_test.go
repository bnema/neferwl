package app

import (
	"context"
	"errors"
	"image"
	"testing"

	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
)

// applyRig drives outputApply the way a backend owner loop does.
type applyRig struct {
	t *testing.T
	a *outputApply
}

func newApplyRig(t *testing.T, headless bool) applyRig {
	mode := ports.OutputMode{Width: 1920, Height: 1080, RefreshMilli: 60000}
	a := newOutputApply(newOutputOverrides(ports.Config{}, headless), logging.For(context.Background(), "app"))
	a.heads(ports.OutputHeads{Heads: []ports.OutputHead{{Info: ports.OutputInfo{Name: "DP-1"}, Enabled: true, Current: &mode, Modes: []ports.OutputMode{mode}}}})
	return applyRig{t, a}
}

// started returns the configuration the backend must start now.
func (r applyRig) started() ports.Config {
	r.t.Helper()
	cfg, ok := r.a.next()
	if !ok {
		r.t.Fatal("no configuration to start")
	}
	if _, again := r.a.next(); again {
		r.t.Fatal("configuration started twice")
	}
	return cfg
}

func (r applyRig) idle() {
	r.t.Helper()
	if cfg, ok := r.a.next(); ok {
		r.t.Fatalf("unexpected configuration to start: %+v", cfg)
	}
}

// core returns the configuration waiting for core, if any.
func (r applyRig) core() (ports.Config, bool) {
	ch := make(chan ports.ConfigChanged, 1)
	out := r.a.configOut(ch)
	if out == nil {
		return ports.Config{}, false
	}
	out <- r.a.config
	r.a.configSent()
	return (<-ch).Config, true
}

func (r applyRig) replies() []ports.OutputApplied {
	var got []ports.OutputApplied
	ch := make(chan ports.OutputApplied, 1)
	for {
		out, next := r.a.replyOut(ch)
		if out == nil {
			return got
		}
		out <- next
		r.a.replySent()
		got = append(got, <-ch)
	}
}

func moveDP1(id uint64, x int) ports.OutputApply {
	return ports.OutputApply{ID: id, Heads: []ports.HeadChange{{Name: "DP-1", Enabled: true, Pos: &image.Point{X: x}}}}
}

func TestOutputApply(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
	}{
		{"ready", nil},
		{"ready error rollback", errors.New("modeset failed")},
		{"timeout rollback", errTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newApplyRig(t, false)
			r.a.request(moveDP1(1, 100))
			if cfg, ok := r.core(); !ok || cfg.Outputs[0].Pos.X != 100 {
				t.Fatalf("core config: %+v", cfg)
			}
			if cfg := r.started(); cfg.Outputs[0].Pos.X != 100 {
				t.Fatalf("started: %+v", cfg)
			}
			if got := r.replies(); len(got) != 0 {
				t.Fatalf("replied before backend finished: %+v", got)
			}
			r.a.finished(tc.failure)
			if tc.failure != nil {
				if got := r.replies(); len(got) != 0 {
					t.Fatalf("replied before rollback: %+v", got)
				}
				if cfg, ok := r.core(); !ok || len(cfg.Outputs) != 0 {
					t.Fatalf("rollback core config: %+v", cfg)
				}
				if cfg := r.started(); len(cfg.Outputs) != 0 {
					t.Fatalf("rollback started: %+v", cfg)
				}
				// A failed rollback still answers with the original failure.
				r.a.finished(errors.New("rollback failed"))
			}
			r.idle()
			got := r.replies()
			if len(got) != 1 || got[0].ID != 1 || !errors.Is(got[0].Err, tc.failure) || (got[0].Err != nil) != (tc.failure != nil) {
				t.Fatalf("replies: %+v, want %v", got, tc.failure)
			}
			if tc.failure != nil && len(r.a.state.effective().Outputs) != 0 {
				t.Fatal("override survived failure")
			}
			r.a.reload(ports.Config{Outputs: []ports.OutputConfig{{Name: "DP-1", Scale: 1.25}}})
			if cfg, ok := r.core(); !ok || cfg.Outputs[0].Pos != nil || cfg.Outputs[0].Scale != 1.25 {
				t.Fatalf("reload core config: %+v", cfg)
			}
			r.started()
			r.a.finished(nil)
			if got := r.replies(); len(got) != 0 {
				t.Fatalf("reload replied: %+v", got)
			}
		})
	}
}

// Consumers that do not read (core, Wayland) never block reloads or requests:
// replies queue in order and core only gets the latest configuration.
func TestOutputApplyOutbox(t *testing.T) {
	r := newApplyRig(t, false)
	r.a.request(moveDP1(1, 100))
	r.started()
	r.a.reload(ports.Config{Outputs: []ports.OutputConfig{{Name: "DP-1", Scale: 1.5}}})
	r.a.reload(ports.Config{Outputs: []ports.OutputConfig{{Name: "DP-1", Scale: 2}}})
	r.a.request(ports.OutputApply{ID: 2})
	r.a.request(ports.OutputApply{ID: 3})
	// The reloads wait for the running configuration: no overlap.
	r.idle()
	r.a.finished(nil)
	// Inventory updates while Wayland is not reading: only the latest waits.
	mode := ports.OutputMode{Width: 1280, Height: 720, RefreshMilli: 60000}
	for _, name := range []string{"DP-1", "DP-2"} {
		r.a.heads(ports.OutputHeads{Heads: []ports.OutputHead{{Info: ports.OutputInfo{Name: name}, Enabled: true, Current: &mode, Modes: []ports.OutputMode{mode}}}})
	}
	headsCh := make(chan ports.OutputHeads, 1)
	out, heads := r.a.headsOut(headsCh)
	if out == nil || len(heads.Heads) != 1 || heads.Heads[0].Info.Name != "DP-2" {
		t.Fatalf("latest heads: %+v", heads)
	}
	out <- heads
	r.a.headsSent()
	if got := <-headsCh; got.Heads[0].Info.Name != "DP-2" {
		t.Fatalf("delivered heads: %+v", got)
	}
	if out, _ := r.a.headsOut(headsCh); out != nil {
		t.Fatal("heads sent twice")
	}
	for i, got := range r.replies() {
		if got.ID != uint64(i+1) || got.Err == nil {
			t.Fatalf("reply %d: %+v", i+1, got)
		}
	}
	if cfg, ok := r.core(); !ok || len(cfg.Outputs) != 1 || cfg.Outputs[0].Scale != 2 || cfg.Outputs[0].Pos != nil {
		t.Fatalf("latest core config: %+v", cfg)
	}
	if _, ok := r.core(); ok {
		t.Fatal("core config sent twice")
	}
	if cfg := r.started(); cfg.Outputs[0].Scale != 2 {
		t.Fatalf("latest backend config: %+v", cfg)
	}
}

func TestOutputSettingsDisableAndHeadlessMode(t *testing.T) {
	mode := ports.OutputMode{Width: 1920, Height: 1080, RefreshMilli: 60000}
	state := newOutputOverrides(ports.Config{}, true)
	state.heads = ports.OutputHeads{Heads: []ports.OutputHead{{Info: ports.OutputInfo{Name: "HEADLESS-1"}, Enabled: true, Current: &mode, Modes: []ports.OutputMode{mode}}}}
	if _, err := state.apply(ports.OutputApply{Heads: []ports.HeadChange{{Name: "HEADLESS-1", Enabled: false}}}); err == nil {
		t.Fatal("headless disable accepted")
	}
	state.headless = false
	cfg, err := state.apply(ports.OutputApply{Heads: []ports.HeadChange{{Name: "HEADLESS-1", Enabled: false}}})
	if err != nil || !cfg.Outputs[0].Off {
		t.Fatalf("disable: %+v, %v", cfg, err)
	}
}

// A reload supersedes a request the backend is applying: the request fails at
// once, and a late failure of its configuration cannot roll back the reload.
func TestReloadDuringPendingApply(t *testing.T) {
	r := newApplyRig(t, false)
	r.a.request(moveDP1(1, 100))
	r.core()
	r.started()
	r.a.reload(ports.Config{Outputs: []ports.OutputConfig{{Name: "DP-1", Scale: 1.5}}})
	if cfg, ok := r.core(); !ok || cfg.Outputs[0].Pos != nil {
		t.Fatalf("override survived reload: %+v", cfg)
	}
	if got := r.replies(); len(got) != 1 || got[0].ID != 1 || !errors.Is(got[0].Err, errApplySuperseded) {
		t.Fatalf("superseded reply: %+v", got)
	}
	// The reload waits for the old configuration, whose failure is dropped.
	r.idle()
	r.a.finished(errors.New("old apply failed"))
	if cfg := r.started(); cfg.Outputs[0].Scale != 1.5 || cfg.Outputs[0].Pos != nil {
		t.Fatalf("reload started: %+v", cfg)
	}
	r.a.finished(errors.New("reload failed"))
	r.idle()
	if _, ok := r.core(); ok {
		t.Fatal("failed reload rolled back")
	}
	if got := r.replies(); len(got) != 0 {
		t.Fatalf("reload replied: %+v", got)
	}
	// A stray result with nothing running is ignored.
	r.a.finished(errors.New("stray"))
	r.idle()
	// The next request is accepted.
	r.a.request(moveDP1(2, 50))
	r.started()
	r.a.finished(nil)
	if got := r.replies(); len(got) != 1 || got[0].ID != 2 || got[0].Err != nil {
		t.Fatalf("next request: %+v", got)
	}
}

// A reload during a rollback supersedes the failed request.
func TestReloadDuringRollback(t *testing.T) {
	r := newApplyRig(t, false)
	r.a.request(moveDP1(1, 100))
	r.started()
	r.a.finished(errors.New("modeset failed"))
	r.started() // rollback
	r.a.reload(ports.Config{Outputs: []ports.OutputConfig{{Name: "DP-1", Scale: 2}}})
	if got := r.replies(); len(got) != 1 || !errors.Is(got[0].Err, errApplySuperseded) {
		t.Fatalf("rollback superseded: %+v", got)
	}
	r.idle()
	r.a.finished(nil) // rollback done
	if cfg := r.started(); cfg.Outputs[0].Scale != 2 {
		t.Fatalf("reload started: %+v", cfg)
	}
	r.a.finished(nil)
	if got := r.replies(); len(got) != 0 {
		t.Fatalf("extra reply: %+v", got)
	}
}

func TestOutputApplyAnsweredAtOnce(t *testing.T) {
	r := newApplyRig(t, false)
	r.a.request(ports.OutputApply{ID: 1, Test: true, Heads: moveDP1(1, 10).Heads})
	r.a.request(ports.OutputApply{ID: 2, Heads: []ports.HeadChange{{Name: "DP-9", Enabled: true}}})
	r.idle()
	if _, ok := r.core(); ok {
		t.Fatal("test or invalid request changed core config")
	}
	got := r.replies()
	if len(got) != 2 || got[0].ID != 1 || got[0].Err != nil || got[1].ID != 2 || got[1].Err == nil {
		t.Fatalf("replies: %+v", got)
	}
	r.a.request(moveDP1(3, 10))
	r.a.request(moveDP1(4, 20))
	if got := r.replies(); len(got) != 1 || got[0].ID != 4 || got[0].Err == nil {
		t.Fatalf("second request while pending: %+v", got)
	}

	h := newApplyRig(t, true)
	h.a.request(ports.OutputApply{ID: 1, Heads: []ports.HeadChange{{Name: "DP-1", Enabled: true, Scale: 2}}})
	var gaps ports.Config
	gaps.Layout.Gaps = 4
	h.a.reload(gaps)
	h.idle()
	if cfg, ok := h.core(); !ok || cfg.Layout.Gaps != 4 {
		t.Fatalf("headless core config: %+v", cfg)
	}
	if got := h.replies(); len(got) != 1 || got[0].ID != 1 || got[0].Err != nil {
		t.Fatalf("headless reply: %+v", got)
	}
}

// A reload that only changes output scales (a saved scale bind) keeps the
// runtime overrides; any other change drops them.
func TestReloadScaleOnlyKeepsOverrides(t *testing.T) {
	mode := ports.OutputMode{Width: 1920, Height: 1080, RefreshMilli: 60000}
	file := ports.Config{Outputs: []ports.OutputConfig{{Name: "DP-1", Scale: 2}}}
	state := newOutputOverrides(file, false)
	state.heads = ports.OutputHeads{Heads: []ports.OutputHead{{Info: ports.OutputInfo{Name: "DP-1"}, Enabled: true, Current: &mode, Modes: []ports.OutputMode{mode}}, {Info: ports.OutputInfo{Name: "DP-2"}, Enabled: true, Current: &mode, Modes: []ports.OutputMode{mode}}}}
	if _, err := state.apply(ports.OutputApply{Heads: []ports.HeadChange{{Name: "DP-1", Enabled: true, Pos: &image.Point{X: 100}}, {Name: "DP-2", Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	scaled := file
	scaled.Outputs = []ports.OutputConfig{{Name: "DP-1", Scale: 1.5}, {Name: "DP-2", Scale: 1.25, ScaleOnly: true, SDRBrightness: ports.DefaultSDRBrightness}}
	cfg := state.reload(scaled)
	if len(cfg.Outputs) != 2 || cfg.Outputs[0].Pos == nil || cfg.Outputs[0].Scale != 1.5 || cfg.Outputs[1].Scale != 1.25 {
		t.Fatalf("scale-only reload: %+v", cfg.Outputs)
	}
	other := scaled
	other.Layout.Gaps = 4
	if cfg := state.reload(other); cfg.Outputs[0].Pos != nil {
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
	state := newOutputOverrides(file, false)
	state.heads = ports.OutputHeads{Heads: []ports.OutputHead{{Info: ports.OutputInfo{Name: "DP-1"}, Enabled: true, Current: &mode, Modes: []ports.OutputMode{mode}}}}
	tr := ports.BufferTransform(3)
	cfg, err := state.apply(ports.OutputApply{Heads: []ports.HeadChange{{Name: "DP-1", Enabled: true, Transform: &tr}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Outputs) != 1 || cfg.Outputs[0].Transform != 3 {
		t.Fatalf("applied transform: %+v", cfg.Outputs)
	}
	scaled := ports.Config{Outputs: []ports.OutputConfig{{Name: "DP-1", Scale: 1.5}}}
	cfg = state.reload(scaled)
	if cfg.Outputs[0].Transform != 3 || cfg.Outputs[0].Scale != 1.5 {
		t.Fatalf("scale reload lost the runtime transform: %+v", cfg.Outputs)
	}
	// Without a transform in the change, the current one stays.
	cfg, err = state.apply(ports.OutputApply{Heads: []ports.HeadChange{{Name: "DP-1", Enabled: true}}})
	if err != nil || cfg.Outputs[0].Transform != 3 {
		t.Fatalf("unset transform changed: %+v, %v", cfg.Outputs, err)
	}
	bad := ports.BufferTransform(8)
	if _, err := state.apply(ports.OutputApply{Heads: []ports.HeadChange{{Name: "DP-1", Enabled: true, Transform: &bad}}}); err == nil {
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
	o := newOutputOverrides(file, false)
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

	cfg, err := o.apply(ports.OutputApply{ID: 1, Heads: []ports.HeadChange{change("DP-1", 100), {Name: "DP-2", Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	dp1, dp2 := byName(cfg, "DP-1"), byName(cfg, "DP-2")
	if dp1.Pos == nil || dp1.Pos.X != 100 || dp2.Pos != nil || dp2.Anchor != anchor {
		t.Fatalf("position on the reference only: DP-1 %+v, DP-2 %+v", dp1, dp2)
	}

	cfg, err = o.apply(ports.OutputApply{ID: 2, Heads: []ports.HeadChange{change("DP-1", 100), change("DP-2", 900)}})
	if err != nil {
		t.Fatal(err)
	}
	dp1, dp2 = byName(cfg, "DP-1"), byName(cfg, "DP-2")
	if dp1.Pos == nil || dp1.Pos.X != 100 || dp2.Pos == nil || dp2.Pos.X != 900 || dp2.Anchor != anchor {
		t.Fatalf("position on both: DP-1 %+v, DP-2 %+v", dp1, dp2)
	}

	// A non scale-only reload drops the overrides: the file relation is back alone.
	file.Keyboard.RepeatRate = 33
	cfg = o.reload(file)
	if dp2 = byName(cfg, "DP-2"); dp2.Pos != nil || dp2.Anchor != anchor || len(cfg.Outputs) != 1 {
		t.Fatalf("after reload: %+v", cfg.Outputs)
	}
}
