package app

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/adapters/clock"
	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/outputkit/cursors"
	"github.com/bnema/neferwl/internal/adapters/xkb"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestControlKeymapFanout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		in, out := make(chan domain.ConfigChanged, 1), make(chan domain.ConfigChanged, 1)
		physical, control := make(chan *xkb.Keymap, 1), make(chan *xkb.Keymap, 1)
		cfg := config.Defaults()
		go relayConfig(ctx, cfg, in, out, physical, make(chan domain.InputDevicesConfig, 1), cursors.New(clock.System{}, 0), make(chan ports.ClientCommand, 4), logging.For(ctx, "config"), control)
		cfg.Keyboard.Layout = "de"
		in <- domain.ConfigChanged{Config: cfg}
		<-out
		a, b := <-physical, <-control
		defer a.Close()
		defer b.Close()
		if a == b || a.String() != b.String() {
			t.Fatal("fanout did not create independent equal maps")
		}
		a.Key(125, true, 0)
		if ev := b.Key(30, true, 0); ev.Mods&domain.ModSuper != 0 {
			t.Fatal("control inherited physical modifiers", ev)
		}
	})
}
