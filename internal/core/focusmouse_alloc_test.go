package core

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/ports"
)

func TestHoverAllocations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := publishRig(t, 1)
		c.cfg.Animations.On = false
		c.pointer = 6
		x, y := hoverPoint(t, c, 6)
		c.cursorX, c.cursorY = x, y
		c.pointerOutput = c.cur().name()
		// No command sink: measure owner work rather than channel bookkeeping.
		c.ch.Commands = nil
		motion := func() {
			if err := c.pointerMotion(context.Background(), ports.PointerMotion{X: x, Y: y}); err != nil {
				t.Fatal(err)
			}
		}
		off := testing.AllocsPerRun(100, motion)
		c.cfg.Focus.FollowMouse = true
		on := testing.AllocsPerRun(100, motion)
		t.Logf("in-window motion: off=%g on=%g", off, on)
		if on != off {
			t.Fatalf("hover added allocations: off=%g on=%g", off, on)
		}
		// Rejected entry: the previous point is in window 6 itself.
		n := testing.AllocsPerRun(100, func() { c.hoverEntry(6, x, y) })
		t.Logf("hoverEntry slid-in=%g", n)
		if n != 0 {
			t.Fatalf("hoverEntry (slid-in) allocated %g", n)
		}
		// Accepted entry: the previous point is in window 5.
		px, py := hoverPoint(t, c, 5)
		if !c.hoverEntry(6, px, py) {
			t.Fatal("entry from window 5 into 6 rejected")
		}
		n = testing.AllocsPerRun(100, func() { c.hoverEntry(6, px, py) })
		t.Logf("hoverEntry accepted=%g", n)
		if n != 0 {
			t.Fatalf("hoverEntry (accepted) allocated %g", n)
		}
	})
}
