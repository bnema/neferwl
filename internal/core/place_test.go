package core_test

import (
	"context"
	"github.com/bnema/neferwl/internal/domain"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/ports"
)

// places collects the PlaceWindow commands queued so far.
func places(commands chan ports.ClientCommand) (out []ports.PlaceWindow) {
	for len(commands) > 0 {
		if v, ok := (<-commands).(ports.PlaceWindow); ok {
			out = append(out, v)
		}
	}
	return out
}

func TestPlaceWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := altCmdDefaults()
		cfg.Border.Width = 0
		cfg.Layout.Gaps = 8
		client := make(chan ports.ClientEvent, 16)
		input := make(chan ports.InputEvent, 16)
		output := make(chan ports.OutputEvent, 8)
		commands := make(chan ports.ClientCommand, 256)
		scenes := make(chan []domain.Scene, 1)
		c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes}, testOptions(t, core.Options{}))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { _ = c.Run(ctx) }()
		output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
		scene(t, scenes)

		// An xdg window is never placed.
		client <- ports.WindowMapped{ID: 1}
		scene(t, scenes)
		if got := places(commands); len(got) != 0 {
			t.Fatal(got)
		}

		// A positioned window is placed once on map.
		client <- ports.WindowMapped{ID: 2, Positioned: true}
		scene(t, scenes)
		first := places(commands)
		if len(first) != 1 || first[0].ID != 2 {
			t.Fatal(first)
		}

		// The same layout sends nothing more.
		client <- ports.WindowMapped{ID: 1}
		scene(t, scenes)
		if got := places(commands); len(got) != 0 {
			t.Fatal(got)
		}

		// A position change sends it again, for the positioned window only.
		client <- ports.WindowMapped{ID: 3}
		scene(t, scenes)
		moved := places(commands)
		if len(moved) != 1 || moved[0].ID != 2 || (moved[0].X == first[0].X && moved[0].Y == first[0].Y) {
			t.Fatal(first, moved)
		}

		// Unmap prunes it: mapping again places from nothing.
		client <- ports.WindowUnmapped{ID: 2}
		scene(t, scenes)
		places(commands)
		client <- ports.WindowMapped{ID: 2, Positioned: true}
		scene(t, scenes)
		again := places(commands)
		if len(again) != 1 || again[0].ID != 2 {
			t.Fatal(again)
		}

	})
}
