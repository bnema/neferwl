package core_test

import (
	"github.com/bnema/neferwl/internal/ports"
	"testing"
)

func TestCascadeSwipeAxes(t *testing.T) {
	r := startSwipe(t, func(c *ports.Config) { c.Layout.Overflow = "cascade"; c.Layout.MaxColumns = 3 })
	for i := 1; i <= 7; i++ {
		r.mapWindow(t, ports.WindowID(i))
	}
	r.begin()
	s := r.move(t, 0, -400)
	band, ok := rectOf(s, 7)
	if !ok || band.X != 0 || band.Y <= 0 {
		t.Fatalf("band swipe must move vertically: %v, shown=%v", band, ok)
	}
	r.end(t, false)
	r.settleAll(t, r.outs...)
	r.begin()
	s = r.move(t, 400, 0)
	workspace, ok := rectOf(s, 7)
	if !ok || workspace.X == 0 || workspace.Y != 0 {
		t.Fatalf("workspace swipe must move horizontally: %v, shown=%v", workspace, ok)
	}
	r.end(t, true)
	r.settleAll(t, r.outs...)
}
