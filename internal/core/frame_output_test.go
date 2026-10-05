package core_test

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

func sceneFor(set []ports.Scene, output string) ports.Scene {
	for _, s := range set {
		if s.Output == output {
			return s
		}
	}
	return ports.Scene{}
}

// A page flip of one output advances only that output's springs: the other
// output keeps its scene (and Seq) until its own flip.
func TestFlipAdvancesOnlyItsOutput(t *testing.T) {
	r := startSwipe(t, nil)
	threeColumns(t, r)
	second := ports.OutputInfo{Name: "DP-2", Width: 800, Height: 600, RefreshMilli: 60000}
	r.plug(t, second)
	r.input <- ports.PointerMotion{X: 1000, Y: 300}
	scene(t, r.scenes)
	for id := ports.WindowID(4); id <= 6; id++ {
		r.mapWindow(t, id)
	}
	// A running landing on each output.
	r.input <- ports.PointerMotion{X: 100, Y: 300}
	scene(t, r.scenes)
	r.flick(t, 10, -40, 0)
	r.input <- ports.PointerMotion{X: 1000, Y: 300}
	scene(t, r.scenes)
	r.flick(t, 10, -40, 0)
	r.drain()

	// The first flip of each output moves its columns.
	r.advance(32 * time.Millisecond)
	r.frames <- ports.OutputFrame{Output: wide.Name}
	set := receive(t, r.scenes)
	a, b := sceneFor(set, wide.Name), sceneFor(set, second.Name)
	r.advance(32 * time.Millisecond)
	r.frames <- ports.OutputFrame{Output: second.Name}
	set = receive(t, r.scenes)
	a2, b2 := sceneFor(set, wide.Name), sceneFor(set, second.Name)
	if a2.Seq != a.Seq || !a2.SameAs(a) {
		t.Fatalf("DP-1 changed on a flip of DP-2: Seq %d, was %d", a2.Seq, a.Seq)
	}
	if b2.Seq <= b.Seq || b2.SameAs(b) {
		t.Fatalf("DP-2 did not move on its own flip: Seq %d, was %d", b2.Seq, b.Seq)
	}

	// And the other way round.
	r.advance(32 * time.Millisecond)
	r.frames <- ports.OutputFrame{Output: wide.Name}
	set = receive(t, r.scenes)
	a3, b3 := sceneFor(set, wide.Name), sceneFor(set, second.Name)
	if a3.Seq <= a2.Seq || a3.SameAs(a2) {
		t.Fatalf("DP-1 did not move on its own flip: Seq %d, was %d", a3.Seq, a2.Seq)
	}
	if b3.Seq != b2.Seq || !b3.SameAs(b2) {
		t.Fatalf("DP-2 changed on a flip of DP-1: Seq %d, was %d", b3.Seq, b2.Seq)
	}
}
