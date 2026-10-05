package core

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// stepScenes steps only the screen and returns the scene of each output.
func stepScenes(t *testing.T, c *Core, only *screen) map[string]ports.Scene {
	t.Helper()
	if err := c.step(context.Background(), only); err != nil {
		t.Fatal(err)
	}
	out := map[string]ports.Scene{}
	for _, s := range <-c.ch.Scenes {
		out[s.Output] = s
	}
	return out
}

// While the focus pulse runs on the focused output, a flip of another output
// keeps the focused output's Seq; its own flip samples the pulse.
func TestFlipOfOtherOutputKeepsPulseSeq(t *testing.T) {
	c, ic := pulseCore(t)
	a := c.cur()
	c.addScreen(ports.OutputInfo{Name: "B", Width: 300, Height: 200})
	b := c.screens[1]
	for id := WindowID(3); id <= 5; id++ {
		b.mon.AddWindow(id)
	}
	indicatorScene(t, c)

	// A pulse on window 1 of A.
	a.mon.Current().FocusID(1)
	indicatorScene(t, c)
	ic.now = ic.now.Add(pulseSettle)
	if !c.pulseTick() {
		t.Fatal("no pulse after settle")
	}
	// A camera transition runs on B.
	w := b.mon.Current()
	w.view.off = 100
	w.view.motion = newMotion(viewSpring(w.view.off, 0), ic.now, 1)

	ic.now = ic.now.Add(pulseRise / 2)
	first := stepScenes(t, c, a)
	if pulseOf(first["A"], 1) <= 0 {
		t.Fatalf("no pulse on A: %v", pulseOf(first["A"], 1))
	}
	ic.now = ic.now.Add(10 * time.Millisecond)
	second := stepScenes(t, c, b)
	if second["A"].Seq != first["A"].Seq || !second["A"].SameAs(first["A"]) {
		t.Fatalf("A changed on a flip of B: Seq %d, was %d", second["A"].Seq, first["A"].Seq)
	}
	if second["B"].SameAs(first["B"]) {
		t.Fatal("B did not move on its own flip")
	}
	ic.now = ic.now.Add(10 * time.Millisecond)
	third := stepScenes(t, c, a)
	if third["A"].Seq <= second["A"].Seq || pulseOf(third["A"], 1) == pulseOf(second["A"], 1) {
		t.Fatalf("A did not sample the pulse on its own flip: Seq %d, was %d", third["A"].Seq, second["A"].Seq)
	}
	if third["B"].Seq != second["B"].Seq {
		t.Fatalf("B changed on a flip of A: Seq %d, was %d", third["B"].Seq, second["B"].Seq)
	}
}
