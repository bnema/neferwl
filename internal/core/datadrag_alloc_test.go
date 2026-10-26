package core

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/ports"
)

func TestDataDragUnchangedRetargetAllocations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := publishRig(t, 1)
		c.cursorX, c.cursorY = 20, 30
		id, x, y := c.hit(c.cursorX, c.cursorY)
		if id == 0 {
			t.Fatal("no target")
		}
		c.dataDrag = &dataDragState{target: id, at: [2]float64{x, y}}
		ctx := context.Background()
		var ev ports.ClientEvent = ports.InputRegionChanged{ID: id, All: true}
		if n := testing.AllocsPerRun(1000, func() {
			if dataDragHitChanged(ev) {
				if err := c.dataDragRetarget(ctx, 0); err != nil {
					panic(err)
				}
			}
		}); n != 0 {
			t.Fatalf("unchanged retarget: %g allocations, want 0", n)
		}
		for _, ev := range []ports.ClientEvent{ports.DataDragIcon{}, ports.WindowAppID{}, ports.CaptureSessionOpen{}, ports.PointerConstrained{}} {
			if dataDragHitChanged(ev) {
				t.Fatalf("rehit for irrelevant event %T", ev)
			}
		}

	})
}
