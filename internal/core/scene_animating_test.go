package core

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// A scene published while a spring runs carries Animating, so its flip wakes
// core for the next frame; the scene the spring settles on does not, and
// neither does any scene of an idle output.
func TestSceneAnimatingFollowsSprings(t *testing.T) {
	c, ic := indicatorCore(t)
	c.cfg.Animations.On = true
	a := c.cur()
	a.mon.AddWindow(1)
	a.mon.AddWindow(2)
	if s := indicatorScene(t, c); s.Animating {
		t.Fatal("an idle output publishes an animating scene")
	}

	// A slide starts.
	w := a.mon.Current()
	w.view.off = 100
	w.view.motion = newMotion(viewSpring(w.view.off, 0), ic.now, 1)
	ic.now = ic.now.Add(10 * time.Millisecond)
	first := stepScenes(t, c, a)["A"]
	if !first.Animating {
		t.Fatal("a scene with a running spring is not animating")
	}
	ic.now = ic.now.Add(10 * time.Millisecond)
	if second := stepScenes(t, c, a)["A"]; !second.Animating || second.Seq == first.Seq {
		t.Fatalf("running slide: animating=%v Seq %d (was %d)", second.Animating, second.Seq, first.Seq)
	}

	// The step that lands the spring publishes the last scene: nothing more
	// is due, so it must not wake core again.
	ic.now = ic.now.Add(10 * time.Second)
	last := stepScenes(t, c, a)["A"]
	if last.Animating {
		t.Fatal("the scene a spring settles on is animating")
	}
	if a.springing() {
		t.Fatal("bad setup: the spring still runs")
	}
	// Idle again: still false, and the Seq is held (no extra recomposition).
	if err := c.publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	idle := (<-c.ch.Scenes)[0]
	if idle.Animating || idle.Seq != last.Seq || !idle.SameAs(last) {
		t.Fatalf("idle publish: animating=%v Seq %d (was %d)", idle.Animating, idle.Seq, last.Seq)
	}
}

// Animating is per output: a spring on one does not mark the other.
func TestSceneAnimatingIsPerOutput(t *testing.T) {
	c, ic := indicatorCore(t)
	c.cfg.Animations.On = true
	a := c.cur()
	c.addScreen(ports.OutputInfo{Name: "B", Width: 300, Height: 200})
	b := c.screens[1]
	a.mon.AddWindow(1)
	b.mon.AddWindow(2)
	indicatorScene(t, c)
	w := b.mon.Current()
	w.view.off = 100
	w.view.motion = newMotion(viewSpring(w.view.off, 0), ic.now, 1)
	ic.now = ic.now.Add(10 * time.Millisecond)
	got := stepScenes(t, c, b)
	if !got["B"].Animating || got["A"].Animating {
		t.Fatalf("animating A=%v B=%v, want B only", got["A"].Animating, got["B"].Animating)
	}
}
