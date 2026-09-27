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
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "rollback"}[failure], func(t *testing.T) {
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
			if failure {
				backendDone <- errors.New("modeset failed")
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
				if (r.Err != nil) != failure {
					t.Fatalf("result: %+v", r)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("missing reply")
			}
			if failure && len(state.effective().Outputs) != 0 {
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
