package core

import (
	"github.com/bnema/neferwl/internal/ports"
	"testing"
	"testing/synctest"
	"time"
)

func TestBranchStartFocus(t *testing.T) {
	for _, focused := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			c := lineageCore(t)
			c.mapWindow(ports.WindowMapped{ID: 1, Proc: lineageNodes(70, 20, 10)})
			c.mapWindow(ports.WindowMapped{ID: 2})
			h := &c.cur().lineageFocus
			at := time.Unix(1000, 0)
			id := WindowID(2)
			if focused {
				id = 1
			}
			h.observe(at.Add(-time.Second), id)
			// User changes focus after the branch starts, before the window maps.
			h.observe(at.Add(time.Second), 3-id)
			game := lineageNodes(60, 50, 40, 30, 20, 10)
			for i := range game {
				game[i].Started = at
			}
			got := c.resolveLauncher(ports.WindowMapped{ID: 3, Proc: game})
			if focused && got != 1 || !focused && got != 0 {
				t.Fatalf("focused at branch=%v launcher=%d", focused, got)
			}
		})
	}
}

func TestFocusHistoryBoundsAndTolerance(t *testing.T) {
	var h focusHistory
	base := time.Unix(1000, 0)
	h.observe(base, 1)
	if h.focusedAt(1, base.Add(-20*time.Millisecond)) || !h.focusedAt(1, base.Add(-lineageTick)) {
		t.Fatal("initial coverage tolerance")
	}
	for i := 1; i <= 40; i++ {
		h.observe(base.Add(time.Duration(i)*time.Second), WindowID(i%2+1))
	}
	if h.focusedAt(1, base) {
		t.Fatal("discarded history accepted")
	}
	if !h.focusedAt(h.current, h.since.Add(time.Hour)) {
		t.Fatal("continuous focus lost")
	}
	if n := testing.AllocsPerRun(100, func() { h.observe(base.Add(time.Hour), 3-h.current) }); n != 0 {
		t.Fatalf("history update allocations: %g", n)
	}
}
