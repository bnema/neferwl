package app

import (
	"context"
	"errors"
	"image"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
)

func TestOutputSettingsRelay(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
	}{
		{"ready", nil},
		{"ready error rollback", errors.New("modeset failed")},
		{"timeout rollback", errTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			state := newOutputOverrides(ports.Config{}, false)
			reloads := make(chan ports.ConfigChanged)
			inventory := make(chan ports.OutputHeads)
			applies := make(chan ports.OutputApply)
			configs := make(chan ports.ConfigChanged, 4)
			backend := make(chan ports.Config, 4)
			backendDone := make(chan error)
			replies := make(chan ports.OutputApplied)
			done := make(chan struct{})
			go func() {
				defer close(done)
				relayOutputSettings(ctx, state, reloads, inventory, applies, configs, backend, backendDone, replies, logging.For(ctx, "app"))
			}()
			defer func() { cancel(); <-done }()
			mode := ports.OutputMode{Width: 1920, Height: 1080, RefreshMilli: 60000}
			inventory <- ports.OutputHeads{Heads: []ports.OutputHead{{Info: ports.OutputInfo{Name: "DP-1"}, Enabled: true, Current: &mode, Modes: []ports.OutputMode{mode}}}}
			applies <- ports.OutputApply{ID: 1, Heads: []ports.HeadChange{{Name: "DP-1", Enabled: true, Pos: &image.Point{X: 100}}}}
			next := <-configs
			if next.Config.Outputs[0].Pos.X != 100 {
				t.Fatalf("next: %+v", next)
			}
			<-backend
			select {
			case <-replies:
				t.Fatal("replied before backend ready")
			default:
			}
			if tc.failure != nil {
				backendDone <- tc.failure
				rollback := <-configs
				if len(rollback.Config.Outputs) != 0 {
					t.Fatalf("rollback: %+v", rollback)
				}
				<-backend
				backendDone <- nil
			} else {
				backendDone <- nil
			}
			select {
			case r := <-replies:
				if !errors.Is(r.Err, tc.failure) || (r.Err != nil) != (tc.failure != nil) {
					t.Fatalf("result: %+v, want %v", r, tc.failure)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("missing reply")
			}
			if tc.failure != nil && len(state.effective().Outputs) != 0 {
				t.Fatal("override survived failure")
			}
			reloads <- ports.ConfigChanged{Config: ports.Config{Outputs: []ports.OutputConfig{{Name: "DP-1", Scale: 1.25}}}}
			reloaded := <-configs
			if reloaded.Config.Outputs[0].Pos != nil || reloaded.Config.Outputs[0].Scale != 1.25 {
				t.Fatalf("reload: %+v", reloaded)
			}
			<-backend
			backendDone <- nil
		})
	}
}

func TestOutputSettingsRelayBlockedConsumers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	state := newOutputOverrides(ports.Config{}, false)
	reloads := make(chan ports.ConfigChanged)
	inventory := make(chan ports.OutputHeads)
	applies := make(chan ports.OutputApply)
	configs := make(chan ports.ConfigChanged)
	backend := make(chan ports.Config)
	backendDone := make(chan error)
	replies := make(chan ports.OutputApplied)
	done := make(chan struct{})
	go func() {
		defer close(done)
		relayOutputSettings(ctx, state, reloads, inventory, applies, configs, backend, backendDone, replies, logging.For(ctx, "app"))
	}()
	defer func() { cancel(); <-done }()

	mode := ports.OutputMode{Width: 1920, Height: 1080, RefreshMilli: 60000}
	select {
	case inventory <- ports.OutputHeads{Heads: []ports.OutputHead{{Info: ports.OutputInfo{Name: "DP-1"}, Enabled: true, Current: &mode}}}:
	case <-time.After(time.Second):
		t.Fatal("inventory blocked")
	}
	select {
	case applies <- ports.OutputApply{ID: 1, Heads: []ports.HeadChange{{Name: "DP-1", Enabled: true, Pos: &image.Point{X: 100}}}}:
	case <-time.After(time.Second):
		t.Fatal("apply blocked")
	}
	// Neither config consumer nor backend nor Wayland is reading. Reload must
	// still supersede the apply and subsequent requests must still be answered.
	select {
	case reloads <- ports.ConfigChanged{Config: ports.Config{Outputs: []ports.OutputConfig{{Name: "DP-1", Scale: 1.5}}}}:
	case <-time.After(time.Second):
		t.Fatal("reload blocked by unread config or backend")
	}
	select {
	case reloads <- ports.ConfigChanged{Config: ports.Config{Outputs: []ports.OutputConfig{{Name: "DP-1", Scale: 2}}}}:
	case <-time.After(time.Second):
		t.Fatal("second reload blocked by unread config or backend")
	}
	for _, id := range []uint64{2, 3} {
		select {
		case applies <- ports.OutputApply{ID: id}:
		case <-time.After(time.Second):
			t.Fatalf("request %d blocked by unread reply", id)
		}
	}
	for _, id := range []uint64{1, 2, 3} {
		select {
		case result := <-replies:
			if result.ID != id || result.Err == nil {
				t.Fatalf("reply %d: %+v", id, result)
			}
		case <-time.After(time.Second):
			t.Fatalf("missing reply %d", id)
		}
	}
	select {
	case cfg := <-configs:
		if len(cfg.Config.Outputs) != 1 || cfg.Config.Outputs[0].Scale != 2 || cfg.Config.Outputs[0].Pos != nil {
			t.Fatalf("latest config: %+v", cfg)
		}
	case <-time.After(time.Second):
		t.Fatal("missing latest config")
	}
	select {
	case cfg := <-backend:
		if len(cfg.Outputs) != 1 || cfg.Outputs[0].Scale != 2 || cfg.Outputs[0].Pos != nil {
			t.Fatalf("latest backend config: %+v", cfg)
		}
	case <-time.After(time.Second):
		t.Fatal("missing latest backend config")
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

func TestReloadDuringPendingApply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := newOutputOverrides(ports.Config{}, false)
	reloads := make(chan ports.ConfigChanged)
	inventory := make(chan ports.OutputHeads)
	applies := make(chan ports.OutputApply)
	configs := make(chan ports.ConfigChanged, 4)
	backend := make(chan ports.Config, 4)
	backendDone := make(chan error)
	replies := make(chan ports.OutputApplied, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		relayOutputSettings(ctx, state, reloads, inventory, applies, configs, backend, backendDone, replies, logging.For(ctx, "app"))
	}()
	defer func() { cancel(); <-done }()
	mode := ports.OutputMode{Width: 1920, Height: 1080, RefreshMilli: 60000}
	inventory <- ports.OutputHeads{Heads: []ports.OutputHead{{Info: ports.OutputInfo{Name: "DP-1"}, Enabled: true, Current: &mode}}}
	applies <- ports.OutputApply{ID: 1, Heads: []ports.HeadChange{{Name: "DP-1", Enabled: true, Pos: &image.Point{X: 100}}}}
	<-configs
	<-backend
	reloads <- ports.ConfigChanged{Config: ports.Config{Outputs: []ports.OutputConfig{{Name: "DP-1", Scale: 1.5}}}}
	select {
	case cfg := <-configs:
		if cfg.Config.Outputs[0].Pos != nil {
			t.Fatal("override survived reload")
		}
	case <-time.After(time.Second):
		t.Fatal("reload blocked by backend")
	}
	if r := <-replies; r.ID != 1 || r.Err == nil {
		t.Fatalf("superseded reply: %+v", r)
	}
	backendDone <- errors.New("old apply failed")
	<-backend
	backendDone <- nil
}
