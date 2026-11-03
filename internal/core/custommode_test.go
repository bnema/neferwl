package core

import (
	"github.com/bnema/neferwl/internal/domain"
	"testing"
)

func TestCustomOutputModes(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		virtual, headless, custom bool
		mode                      *domain.OutputMode
		valid                     bool
	}{
		{"virtual", true, false, true, &domain.OutputMode{Width: 2560, Height: 1440, RefreshMilli: 120000}, true},
		{"headless virtual default", true, true, true, &domain.OutputMode{Width: 1, Height: 16384}, true},
		{"physical custom", false, false, true, &domain.OutputMode{Width: 1920, Height: 1080, RefreshMilli: 60000}, false},
		{"physical listed", false, false, false, &domain.OutputMode{Width: 1920, Height: 1080, RefreshMilli: 60000}, true},
		{"physical unlisted", false, false, false, &domain.OutputMode{Width: 1280, Height: 720}, false},
		{"virtual unlisted noncustom", true, false, false, &domain.OutputMode{Width: 1280, Height: 720}, false},
		{"zero width", true, false, true, &domain.OutputMode{Width: 0, Height: 10}, false},
		{"negative height", true, false, true, &domain.OutputMode{Width: 10, Height: -1}, false},
		{"wide", true, false, true, &domain.OutputMode{Width: 16385, Height: 10}, false},
		{"tall", true, false, true, &domain.OutputMode{Width: 10, Height: 16385}, false},
		{"negative refresh", true, false, true, &domain.OutputMode{Width: 10, Height: 10, RefreshMilli: -1}, false},
		{"missing mode", true, false, true, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := NewOutputOverrides(domain.Config{Outputs: []domain.OutputConfig{{Name: "tv", Virtual: tc.virtual}}}, tc.headless)
			listed := domain.OutputMode{Width: 1920, Height: 1080, RefreshMilli: 60000}
			state.SetHeads(domain.OutputHeads{Heads: []domain.OutputHead{{Info: domain.OutputInfo{Name: "tv", Virtual: tc.virtual}, Enabled: true, Current: &listed, Modes: []domain.OutputMode{listed}}}})
			req := domain.OutputApply{Heads: []domain.HeadChange{{Name: "tv", Enabled: true, Mode: tc.mode, Custom: tc.custom}}}
			req.Test = true
			if _, err := state.Apply(req); (err == nil) != tc.valid {
				t.Fatalf("test validation: %v", err)
			}
			if len(state.Snapshot()) != 0 {
				t.Fatal("test mutated overrides")
			}
			req.Test = false
			cfg, err := state.Apply(req)
			if (err == nil) != tc.valid {
				t.Fatalf("apply validation: %v", err)
			}
			if tc.valid && tc.custom && tc.mode.RefreshMilli == 0 && cfg.Outputs[0].Mode != "1x16384@60.000" {
				t.Fatalf("default refresh: %+v", cfg.Outputs)
			}
		})
	}
}

func TestHeadlessGuardExemptsVirtual(t *testing.T) {
	for _, virtual := range []bool{false, true} {
		state := NewOutputOverrides(domain.Config{}, true)
		state.SetHeads(domain.OutputHeads{Heads: []domain.OutputHead{{Info: domain.OutputInfo{Name: "HEADLESS-1", Virtual: virtual}, Enabled: true}}})
		_, err := state.Apply(domain.OutputApply{Heads: []domain.HeadChange{{Name: "HEADLESS-1", Enabled: false}}})
		if (err == nil) != virtual {
			t.Fatalf("virtual=%v error=%v", virtual, err)
		}
	}
}
