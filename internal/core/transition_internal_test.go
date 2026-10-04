package core

import (
	"testing"
	"time"
)

// The focus pulse waits while a camera transition of a key runs, and starts
// once it settled.
func TestPulseWaitsForCameraTransition(t *testing.T) {
	c, ic := pulseCore(t)
	m := c.cur().mon
	for id := WindowID(3); id <= 4; id++ {
		m.AddWindow(id)
	}
	indicatorScene(t, c)
	ic.now = ic.now.Add(pulseSettle + pulseCooldown)
	c.pulseTick()
	indicatorScene(t, c)

	before := c.snapshot()
	for range 3 {
		c.applyAction(ActionFocusColumnLeft)
	}
	c.transition(before, ic.now)
	w := m.Current()
	if !w.motion.on || m.settled() {
		t.Fatal("no camera transition after the focus moved the view")
	}
	indicatorScene(t, c)
	ic.now = ic.now.Add(pulseSettle)
	if c.pulseTick() {
		t.Fatal("pulse started while the view slides")
	}
	c.animate(ic.now.Add(5*time.Second), nil)
	ic.now = ic.now.Add(5 * time.Second)
	if !m.settled() {
		t.Fatal("view still slides")
	}
	ic.now = ic.now.Add(pulseSettle)
	if !c.pulseTick() {
		t.Fatal("no pulse once the view settled")
	}
}
