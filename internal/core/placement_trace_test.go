package core

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestPlacementTraceFirstMap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		traces := make(chan domain.PlacementTrace, 4)
		cfg := domain.Config{}
		cfg.Keyboard.CmdKey = "alt"
		cfg.Layout.MaxColumns = 1
		c, err := New(cfg, Channels{Placements: traces, Scenes: make(chan []domain.Scene, 1)}, testOptions(t, Options{}))
		if err != nil {
			t.Fatal(err)
		}
		c.clientEvent(context.Background(), ports.WindowMapped{ID: 1})
		select {
		case got := <-traces:
			if got.Window != 1 || got.Path != "focused" || got.Workspace == 0 || got.Launcher != 0 || got.Cover != 0 || !got.WorkspaceFocusChanged || !got.SeatFocusChanged {
				t.Fatalf("trace: %+v", got)
			}
		default:
			t.Fatal("missing trace")
		}
		c.clientEvent(context.Background(), ports.WindowMapped{ID: 1, Remap: true})
		select {
		case got := <-traces:
			t.Fatalf("remap trace: %+v", got)
		default:
		}
	})
}
