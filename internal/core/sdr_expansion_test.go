package core

import (
	"context"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"math"
	"testing"
	"testing/synctest"
)

func TestDecideSDRExpansion(t *testing.T) {
	game, on, off := domain.SDRExpandGame, domain.SDRExpandOn, domain.SDRExpandForceOff
	zero, peak, intensity, decode := 0.0, 800.0, 0.8, domain.SDRDecodeSRGB
	base := sdrExpansionInput{Output: domain.OutputHDRCaps{Active: true, Caps: domain.ResolveHDRCaps(domain.HDRCapsInput{Capable: true, MaxLuminance: 604})}, Config: domain.OutputConfig{SDRExpandIntensity: 0.5, SDRBrightness: 203}, Fullscreen: true, Game: true}
	for _, tt := range []struct {
		name   string
		edit   func(*sdrExpansionInput)
		active bool
		reason string
	}{
		{"LG", func(*sdrExpansionInput) {}, true, "eligible"},
		{"colour managed", func(i *sdrExpansionInput) { i.ColorDescription = true }, false, "colour-managed surface"},
		{"forced colour managed", func(i *sdrExpansionInput) {
			i.ColorDescription = true
			i.Config.SDRExpand = domain.SDRExpandAlways
			i.Rule.sdrExpand = &on
		}, false, "colour-managed surface"},
		{"Gigabyte", func(i *sdrExpansionInput) { i.Output.Caps.PeakNits = 446 }, true, "eligible"},
		{"assumed", func(i *sdrExpansionInput) { i.Output.Caps = domain.ResolveHDRCaps(domain.HDRCapsInput{Capable: true}) }, true, "eligible"},
		{"inactive", func(i *sdrExpansionInput) { i.Output.Active = false }, false, "output not HDR"},
		{"unavailable", func(i *sdrExpansionInput) { i.Output.Caps.Tier = domain.HDRUnavailable }, false, "unavailable"},
		{"off", func(i *sdrExpansionInput) { i.Config.SDRExpand = domain.SDRExpandOff }, false, "mode off"},
		{"popup", func(i *sdrExpansionInput) { i.Popup = true }, false, "popup"},
		{"headroom", func(i *sdrExpansionInput) { i.Output.Caps.PeakNits = 250 }, false, "insufficient headroom"},
		{"not fullscreen", func(i *sdrExpansionInput) { i.Fullscreen = false }, false, "not fullscreen"},
		{"not game", func(i *sdrExpansionInput) { i.Game = false }, false, "not game"},
		{"rule game", func(i *sdrExpansionInput) { i.Game = false; i.Rule.sdrExpand = &game }, true, "eligible"},
		{"always", func(i *sdrExpansionInput) {
			i.Config.SDRExpand = domain.SDRExpandAlways
			i.Game = false
			i.Fullscreen = false
			i.Output.Caps.PeakNits = 250
		}, true, "eligible"},
		{"rule on", func(i *sdrExpansionInput) {
			i.Rule.sdrExpand = &on
			i.Game = false
			i.Fullscreen = false
			i.Output.Caps.PeakNits = 250
		}, true, "eligible"},
		{"rule off beats always", func(i *sdrExpansionInput) { i.Config.SDRExpand = domain.SDRExpandAlways; i.Rule.sdrExpand = &off }, false, "rule off"},
		{"forced no headroom", func(i *sdrExpansionInput) { i.Config.SDRExpand = domain.SDRExpandAlways; i.Output.Caps.PeakNits = 203 }, false, "insufficient headroom"},
		{"zero", func(i *sdrExpansionInput) { i.Rule.sdrExpandIntensity = &zero }, false, "zero intensity"},
		{"invalid", func(i *sdrExpansionInput) { i.Output.Caps.PeakNits = math.NaN() }, false, "invalid peak"},
		{"overrides", func(i *sdrExpansionInput) {
			i.Config.SDRExpandPeak = 600
			i.Rule.sdrExpandPeak = &peak
			i.Rule.sdrExpandIntensity = &intensity
			i.Rule.sdrDecode = &decode
		}, true, "eligible"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := base
			tt.edit(&in)
			curve, reason := decideSDRExpansion(in)
			if curve.Active() != tt.active || reason != tt.reason {
				t.Fatalf("%+v: %s", curve, reason)
			}
			if tt.name == "LG" && math.Abs(curve.Top-(1+0.5*(604.0/203-1))) > 1e-9 {
				t.Fatal(curve)
			}
			if tt.name == "overrides" && (curve.Decode != decode || math.Abs(curve.Top-(1+0.8*(800.0/203-1))) > 1e-9) {
				t.Fatal(curve)
			}
		})
	}
}

func TestSDRExpansionSceneReevaluation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {

		client := make(chan ports.ClientEvent)
		outputs := make(chan ports.OutputEvent)
		reload := make(chan domain.ConfigChanged)
		scenes := make(chan []domain.Scene, 1)
		cfg := domain.Config{}
		cfg.Layout.MaxColumns = 2
		cfg.Keyboard.CmdKey = "super"
		c, err := New(cfg, Channels{Client: client, Output: outputs, Config: reload, Scenes: scenes, Commands: make(chan ports.ClientCommand, 128)}, testOptions(t, Options{}))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- c.Run(ctx) }()
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}()
		check := func(want bool) {
			t.Helper()
			synctest.Wait()
			select {
			case ss := <-scenes:
				for _, s := range ss {
					for _, w := range s.Windows {
						if w.ID == 1 {
							if w.SDRExpansion.Active() != want {
								t.Fatalf("curve %+v, want active %t", w.SDRExpansion, want)
							}
							return
						}
					}
				}
				t.Fatal("window missing")
			default:
				t.Fatal("no scene published")
			}
		}
		outputs <- ports.OutputAdded{Info: domain.OutputInfo{Name: "A", Width: 300, Height: 200}}
		synctest.Wait()
		<-scenes
		client <- ports.WindowMapped{ID: 1, AppID: "game", Fullscreen: true, ContentType: domain.ContentGame}
		check(false)
		event := domain.OutputHDRCaps{Output: "A", Active: true, Caps: domain.ResolveHDRCaps(domain.HDRCapsInput{Capable: true, MaxLuminance: 604})}
		outputs <- ports.OutputHDRCaps{OutputHDRCaps: event}
		check(true)
		event.Active = false
		outputs <- ports.OutputHDRCaps{OutputHDRCaps: event}
		check(false)
		event.Active = true
		outputs <- ports.OutputHDRCaps{OutputHDRCaps: event}
		check(true)
		client <- ports.WindowContentType{ID: 1, Type: domain.ContentNone}
		check(false)
		client <- ports.WindowContentType{ID: 1, Type: domain.ContentGame}
		check(true)
		cfg.Outputs = []domain.OutputConfig{{Name: "A", ScaleOnly: true, SDRExpand: domain.SDRExpandOff, SDRExpandIntensity: 0.5}}
		reload <- domain.ConfigChanged{Config: cfg}
		check(false)
		cfg.Outputs = nil
		reload <- domain.ConfigChanged{Config: cfg}
		check(true)
		client <- ports.WindowContentType{ID: 1, Type: domain.ContentNone}
		check(false)
		client <- ports.WindowAppID{ID: 1, AppID: "steam_app_123"}
		check(true)
		client <- ports.WindowAppID{ID: 1, AppID: "other"}
		check(false)
	})
}
