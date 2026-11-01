package core

import (
	"context"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"testing"
	"testing/synctest"
)

func TestOutputHDRCapsLifecycleOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		outputs := make(chan ports.OutputEvent, 16)
		cfg := domain.Config{}
		cfg.Layout.MaxColumns = 2
		cfg.Keyboard.CmdKey = "super"
		c, err := New(cfg, Channels{Output: outputs, Scenes: make(chan []domain.Scene, 1), Commands: make(chan ports.ClientCommand, 128)}, testOptions(t, Options{}))
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
		old := domain.OutputHDRCaps{Output: "A", Active: true, Caps: domain.ResolveHDRCaps(domain.HDRCapsInput{Capable: true, MaxLuminance: 604})}
		next := old
		next.Caps = domain.ResolveHDRCaps(domain.HDRCapsInput{Capable: true, MaxLuminance: 446})
		// Queue whole lifetimes without waiting between add/caps/remove/reconnect.
		outputs <- ports.OutputAdded{Info: domain.OutputInfo{Name: "A", Width: 100, Height: 100}}
		outputs <- ports.OutputHDRCaps{OutputHDRCaps: old}
		outputs <- ports.OutputRemoved{Name: "A"}
		outputs <- ports.OutputHDRCaps{OutputHDRCaps: old} // unknown output cannot resurrect
		outputs <- ports.OutputAdded{Info: domain.OutputInfo{Name: "A", Width: 100, Height: 100}}
		outputs <- ports.OutputHDRCaps{OutputHDRCaps: next}
		synctest.Wait()
		if got, ok := c.outputHDRCaps("A"); !ok || got != next {
			t.Fatalf("new lifetime: %+v %t", got, ok)
		}
		outputs <- ports.OutputRemoved{Name: "A"}
		synctest.Wait()
		if _, ok := c.outputHDRCaps("A"); ok {
			t.Fatal("removed caps retained")
		}
	})
}
