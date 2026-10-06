package core_test

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
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
	if !ok || workspace.X >= 0 || workspace.Y != 0 {
		t.Fatalf("workspace swipe must move horizontally: %v, shown=%v", workspace, ok)
	}
	r.end(t, true)
	r.settleAll(t, r.outs...)
}

func TestCascadeDiscreteSwipeRevealsBand(t *testing.T) {
	r := startSwipe(t, func(c *ports.Config) { c.Layout.Overflow = "cascade"; c.Layout.MaxColumns = 3; c.Animations.On = false })
	for i := 1; i <= 4; i++ {
		r.mapWindow(t, ports.WindowID(i))
	}
	r.begin()
	for range 10 {
		r.at += 8 * time.Millisecond
		r.input <- ports.SwipeUpdate{DY: -40, Time: r.at}
	}
	s := r.end(t, false)
	p, ok := rectOf(s, 1)
	if !ok || p.Y != 0 {
		t.Fatalf("band not revealed: %v shown=%v", p, ok)
	}
}
