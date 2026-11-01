package core

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestOutputHDRCapsStoredAndRemoved(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		events := make(chan domain.OutputHDRCaps)
		outputs := make(chan ports.OutputEvent)
		cfg := domain.Config{}
		cfg.Layout.MaxColumns = 2
		cfg.Keyboard.CmdKey = "super"
		c, err := New(cfg, Channels{HDRCaps: events, Output: outputs, Scenes: make(chan []domain.Scene, 1), Commands: make(chan ports.ClientCommand, 64)}, testOptions(t, Options{}))
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
		outputs <- ports.OutputAdded{Info: domain.OutputInfo{Name: "A", Width: 100, Height: 100}}
		synctest.Wait()
		want := domain.OutputHDRCaps{Output: "A", Caps: domain.ResolveHDRCaps(domain.HDRCapsInput{Capable: true, MaxLuminance: 604}), Active: true}
		events <- want
		synctest.Wait()
		if got, ok := c.outputHDRCaps("A"); !ok || got != want {
			t.Fatalf("stored %v, %t", got, ok)
		}
		other := want
		other.Output = "B"
		other.Active = false
		events <- other
		synctest.Wait()
		want.Active = false
		events <- want
		synctest.Wait()
		if got, _ := c.outputHDRCaps("A"); got != want {
			t.Fatal("update lost", got)
		}
		outputs <- ports.OutputRemoved{Name: "A"}
		synctest.Wait()
		if _, ok := c.outputHDRCaps("A"); ok {
			t.Fatal("removed output retained")
		}
		if got, ok := c.outputHDRCaps("B"); !ok || got != other {
			t.Fatal("other output lost")
		}
		close(events)
		synctest.Wait()
		if c.ch.HDRCaps != nil {
			t.Fatal("closed channel retained")
		}
	})
}
