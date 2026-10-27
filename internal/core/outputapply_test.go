package core

import (
	"errors"
	"image"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

// applyRig drives OutputApply the way a backend owner loop does.
type applyRig struct {
	t *testing.T
	a *OutputApply
}

func newApplyRig(t *testing.T, headless bool) applyRig {
	mode := domain.OutputMode{Width: 1920, Height: 1080, RefreshMilli: 60000}
	a := NewOutputApply(NewOutputOverrides(domain.Config{}, headless))
	a.Heads(domain.OutputHeads{Heads: []domain.OutputHead{{Info: domain.OutputInfo{Name: "DP-1"}, Enabled: true, Current: &mode, Modes: []domain.OutputMode{mode}}}})
	return applyRig{t, a}
}

// started returns the configuration the backend must start now.
func (r applyRig) Started() domain.Config {
	r.t.Helper()
	cfg, ok := r.a.Next()
	if !ok {
		r.t.Fatal("no configuration to start")
	}
	if _, again := r.a.Next(); again {
		r.t.Fatal("configuration started twice")
	}
	return cfg
}

func (r applyRig) idle() {
	r.t.Helper()
	if cfg, ok := r.a.Next(); ok {
		r.t.Fatalf("unexpected configuration to start: %+v", cfg)
	}
}

// core returns the configuration waiting for core, if any.
func (r applyRig) core() (domain.Config, bool) {
	ch := make(chan domain.ConfigChanged, 1)
	out := r.a.ConfigOut(ch)
	if out == nil {
		return domain.Config{}, false
	}
	out <- r.a.config
	r.a.ConfigSent()
	return (<-ch).Config, true
}

func (r applyRig) replies() []domain.OutputApplied {
	var got []domain.OutputApplied
	ch := make(chan domain.OutputApplied, 1)
	for {
		out, next := r.a.ReplyOut(ch)
		if out == nil {
			return got
		}
		out <- next
		r.a.ReplySent()
		got = append(got, <-ch)
	}
}

func moveDP1(id uint64, x int) domain.OutputApply {
	return domain.OutputApply{ID: id, Heads: []domain.HeadChange{{Name: "DP-1", Enabled: true, Pos: &image.Point{X: x}}}}
}

func TestOutputApply(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
	}{
		{"ready", nil},
		{"ready error rollback", errors.New("modeset failed")},
		{"timeout rollback", ErrApplyTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newApplyRig(t, false)
			r.a.Request(moveDP1(1, 100))
			if cfg, ok := r.core(); !ok || cfg.Outputs[0].Pos.X != 100 {
				t.Fatalf("core config: %+v", cfg)
			}
			if cfg := r.Started(); cfg.Outputs[0].Pos.X != 100 {
				t.Fatalf("started: %+v", cfg)
			}
			if got := r.replies(); len(got) != 0 {
				t.Fatalf("replied before backend finished: %+v", got)
			}
			decision := r.a.Finished(tc.failure)
			if (len(decision.Applied) != 0) != (tc.failure == nil) {
				t.Fatalf("applied heads: %+v", decision)
			}
			if tc.failure != nil {
				if got := r.replies(); len(got) != 0 {
					t.Fatalf("replied before rollback: %+v", got)
				}
				if cfg, ok := r.core(); !ok || len(cfg.Outputs) != 0 {
					t.Fatalf("rollback core config: %+v", cfg)
				}
				if cfg := r.Started(); len(cfg.Outputs) != 0 {
					t.Fatalf("rollback started: %+v", cfg)
				}
				// A failed rollback still answers with the original failure.
				r.a.Finished(errors.New("rollback failed"))
			}
			r.idle()
			got := r.replies()
			if len(got) != 1 || got[0].ID != 1 || !errors.Is(got[0].Err, tc.failure) || (got[0].Err != nil) != (tc.failure != nil) {
				t.Fatalf("replies: %+v, want %v", got, tc.failure)
			}
			if tc.failure != nil && len(r.a.state.Effective().Outputs) != 0 {
				t.Fatal("override survived failure")
			}
			r.a.Reload(domain.Config{Outputs: []domain.OutputConfig{{Name: "DP-1", Scale: 1.25}}})
			if cfg, ok := r.core(); !ok || cfg.Outputs[0].Pos != nil || cfg.Outputs[0].Scale != 1.25 {
				t.Fatalf("reload core config: %+v", cfg)
			}
			r.Started()
			r.a.Finished(nil)
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
	r.a.Request(moveDP1(1, 100))
	r.Started()
	r.a.Reload(domain.Config{Outputs: []domain.OutputConfig{{Name: "DP-1", Scale: 1.5}}})
	r.a.Reload(domain.Config{Outputs: []domain.OutputConfig{{Name: "DP-1", Scale: 2}}})
	r.a.Request(domain.OutputApply{ID: 2})
	r.a.Request(domain.OutputApply{ID: 3})
	// The reloads wait for the running configuration: no overlap.
	r.idle()
	r.a.Finished(nil)
	// Inventory updates while Wayland is not reading: only the latest waits.
	mode := domain.OutputMode{Width: 1280, Height: 720, RefreshMilli: 60000}
	for _, name := range []string{"DP-1", "DP-2"} {
		r.a.Heads(domain.OutputHeads{Heads: []domain.OutputHead{{Info: domain.OutputInfo{Name: name}, Enabled: true, Current: &mode, Modes: []domain.OutputMode{mode}}}})
	}
	headsCh := make(chan domain.OutputHeads, 1)
	out, heads := r.a.HeadsOut(headsCh)
	if out == nil || len(heads.Heads) != 1 || heads.Heads[0].Info.Name != "DP-2" {
		t.Fatalf("latest heads: %+v", heads)
	}
	out <- heads
	r.a.HeadsSent()
	if got := <-headsCh; got.Heads[0].Info.Name != "DP-2" {
		t.Fatalf("delivered heads: %+v", got)
	}
	if out, _ := r.a.HeadsOut(headsCh); out != nil {
		t.Fatal("heads sent twice")
	}
	replies := r.replies()
	if len(replies) != 3 {
		t.Fatalf("replies: got %d, want 3: %+v", len(replies), replies)
	}
	for i, got := range replies {
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
	if cfg := r.Started(); cfg.Outputs[0].Scale != 2 {
		t.Fatalf("latest backend config: %+v", cfg)
	}
}

// A reload supersedes a request the backend is applying: the request fails at
// once, and a late failure of its configuration cannot roll back the reload.
func TestReloadDuringPendingApply(t *testing.T) {
	r := newApplyRig(t, false)
	r.a.Request(moveDP1(1, 100))
	r.core()
	r.Started()
	r.a.Reload(domain.Config{Outputs: []domain.OutputConfig{{Name: "DP-1", Scale: 1.5}}})
	if cfg, ok := r.core(); !ok || cfg.Outputs[0].Pos != nil {
		t.Fatalf("override survived reload: %+v", cfg)
	}
	if got := r.replies(); len(got) != 1 || got[0].ID != 1 || !errors.Is(got[0].Err, ErrApplySuperseded) {
		t.Fatalf("superseded reply: %+v", got)
	}
	// The reload waits for the old configuration, whose failure is dropped.
	r.idle()
	r.a.Finished(errors.New("old apply failed"))
	if cfg := r.Started(); cfg.Outputs[0].Scale != 1.5 || cfg.Outputs[0].Pos != nil {
		t.Fatalf("reload started: %+v", cfg)
	}
	r.a.Finished(errors.New("reload failed"))
	r.idle()
	if _, ok := r.core(); ok {
		t.Fatal("failed reload rolled back")
	}
	if got := r.replies(); len(got) != 0 {
		t.Fatalf("reload replied: %+v", got)
	}
	// A stray result with nothing running is ignored.
	r.a.Finished(errors.New("stray"))
	r.idle()
	// The next request is accepted.
	r.a.Request(moveDP1(2, 50))
	r.Started()
	r.a.Finished(nil)
	if got := r.replies(); len(got) != 1 || got[0].ID != 2 || got[0].Err != nil {
		t.Fatalf("next request: %+v", got)
	}
}

// A reload during a rollback supersedes the failed request.
func TestReloadDuringRollback(t *testing.T) {
	r := newApplyRig(t, false)
	r.a.Request(moveDP1(1, 100))
	r.Started()
	r.a.Finished(errors.New("modeset failed"))
	r.Started() // rollback
	r.a.Reload(domain.Config{Outputs: []domain.OutputConfig{{Name: "DP-1", Scale: 2}}})
	if got := r.replies(); len(got) != 1 || !errors.Is(got[0].Err, ErrApplySuperseded) {
		t.Fatalf("rollback superseded: %+v", got)
	}
	r.idle()
	r.a.Finished(nil) // rollback done
	if cfg := r.Started(); cfg.Outputs[0].Scale != 2 {
		t.Fatalf("reload started: %+v", cfg)
	}
	r.a.Finished(nil)
	if got := r.replies(); len(got) != 0 {
		t.Fatalf("extra reply: %+v", got)
	}
}

func TestOutputApplyAnsweredAtOnce(t *testing.T) {
	r := newApplyRig(t, false)
	r.a.Request(domain.OutputApply{ID: 1, Test: true, Heads: moveDP1(1, 10).Heads})
	r.a.Request(domain.OutputApply{ID: 2, Heads: []domain.HeadChange{{Name: "DP-9", Enabled: true}}})
	r.idle()
	if _, ok := r.core(); ok {
		t.Fatal("test or invalid request changed core config")
	}
	got := r.replies()
	if len(got) != 2 || got[0].ID != 1 || got[0].Err != nil || got[1].ID != 2 || got[1].Err == nil {
		t.Fatalf("replies: %+v", got)
	}
	r.a.Request(moveDP1(3, 10))
	r.a.Request(moveDP1(4, 20))
	if got := r.replies(); len(got) != 1 || got[0].ID != 4 || got[0].Err == nil {
		t.Fatalf("second request while pending: %+v", got)
	}

	h := newApplyRig(t, true)
	h.a.Request(domain.OutputApply{ID: 1, Heads: []domain.HeadChange{{Name: "DP-1", Enabled: true, Scale: 2}}})
	var gaps domain.Config
	gaps.Layout.Gaps = 4
	h.a.Reload(gaps)
	h.idle()
	if cfg, ok := h.core(); !ok || cfg.Layout.Gaps != 4 {
		t.Fatalf("headless core config: %+v", cfg)
	}
	if got := h.replies(); len(got) != 1 || got[0].ID != 1 || got[0].Err != nil {
		t.Fatalf("headless reply: %+v", got)
	}
}
