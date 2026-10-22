package core

import (
	"context"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func unmanagedScene(t *testing.T, c *Core, i int) ports.Scene {
	t.Helper()
	s, err := c.sceneFor(context.Background(), i, c.screens[i], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mapUnmanaged(t *testing.T, c *Core, id WindowID, x, y, w, h int, noInput bool) {
	t.Helper()
	c.clientEvent(context.Background(), ports.UnmanagedMapped{ID: id, X: x, Y: y, Width: w, Height: h, NoInput: noInput})
}

func TestUnmanagedScene(t *testing.T) {
	c := publishRig(t, 2)
	mapUnmanaged(t, c, 100, 310, 20, 50, 40, false)
	mapUnmanaged(t, c, 101, 5, 5, 10, 10, true)
	b := unmanagedScene(t, c, 1)
	last := b.Windows[len(b.Windows)-1]
	if last.ID != 100 || last.Rect != (Rect{X: 10, Y: 20, W: 50, H: 40}) || !last.Popup || !last.OverLayers {
		t.Fatalf("screen B top window %+v", last)
	}
	a := unmanagedScene(t, c, 0)
	if got := a.Windows[len(a.Windows)-1]; got.ID != 101 || got.Rect.X != 5 {
		t.Fatalf("screen A top window %+v", got)
	}
	c.clientEvent(context.Background(), ports.UnmanagedMoved{ID: 100, X: 20, Y: 20, Width: 50, Height: 40})
	if got := unmanagedScene(t, c, 0).Windows; got[len(got)-2].ID != 100 || got[len(got)-2].Rect.X != 20 {
		t.Fatalf("moved window not on screen A: %+v", got)
	}
	c.clientEvent(context.Background(), ports.UnmanagedUnmapped{ID: 100})
	c.clientEvent(context.Background(), ports.UnmanagedUnmapped{ID: 101})
	if len(c.unmanagedOrder) != 0 || len(c.unmanaged) != 0 {
		t.Fatal("state left after unmap")
	}
}

func TestUnmanagedHiddenWhilePreviewing(t *testing.T) {
	c := publishRig(t, 1)
	mapUnmanaged(t, c, 100, 10, 10, 50, 40, false)
	c.cur().mon.ov.open = true
	if !c.cur().mon.previewing() {
		t.Fatal("setup: not previewing")
	}
	for _, w := range unmanagedScene(t, c, 0).Windows {
		if w.ID == 100 {
			t.Fatal("unmanaged window drawn in the overview")
		}
	}
	if id, _, _ := c.hit(20, 20); id == 100 {
		t.Fatal("unmanaged window hit in the overview")
	}
}

func TestUnmanagedHit(t *testing.T) {
	c := publishRig(t, 2)
	mapUnmanaged(t, c, 100, 310, 20, 50, 40, false)
	mapUnmanaged(t, c, 101, 320, 30, 10, 10, false)
	mapUnmanaged(t, c, 102, 340, 30, 10, 10, true)
	if id, x, y := c.hit(325, 35); id != 101 || x != 5 || y != 5 {
		t.Fatalf("topmost: %d %v %v", id, x, y)
	}
	if id, _, _ := c.hit(345, 35); id != 100 {
		t.Fatalf("noInput must be skipped: %d", id)
	}
	c.windows.setRegion(ports.InputRegionChanged{ID: 100, Rects: []Rect{{X: 0, Y: 0, W: 5, H: 5}}})
	if id, _, _ := c.hit(345, 35); id == 100 {
		t.Fatal("outside the input region")
	}
	c.constraint.Mode = ports.ConstraintLock
	if id, _, _ := c.hit(325, 35); id == 101 {
		t.Fatal("locked pointer must not move to an unmanaged window")
	}
	c.constraint.Mode = 0
	c.security.Protected = true
	if id, _, _ := c.hit(325, 35); id == 101 {
		t.Fatal("protected session hit an unmanaged window")
	}
}

func unmanagedRig(t testing.TB, n int) *Core {
	c := publishRig(t, 1)
	for i := range n {
		c.clientEvent(context.Background(), ports.UnmanagedMapped{ID: WindowID(100 + i), X: 10 * i, Y: 10, Width: 30, Height: 30})
	}
	return c
}

func publishOnce(t testing.TB, c *Core) {
	if err := c.publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-c.ch.Scenes
}

// TestUnmanagedPublishAllocations: three unmanaged windows cost no more than
// none (the scene window slice is presized for them).
func TestUnmanagedPublishAllocations(t *testing.T) {
	base, with := unmanagedRig(t, 0), unmanagedRig(t, 3)
	publishOnce(t, base)
	publishOnce(t, with)
	a := testing.AllocsPerRun(50, func() { publishOnce(t, base) })
	b := testing.AllocsPerRun(50, func() { publishOnce(t, with) })
	if b > a {
		t.Fatalf("publish allocs with unmanaged = %v, without = %v", b, a)
	}
}

func TestUnmanagedHitAllocations(t *testing.T) {
	c := unmanagedRig(t, 3)
	if n := testing.AllocsPerRun(100, func() { c.hit(25, 20); c.hit(250, 150) }); n != 0 {
		t.Fatalf("hit allocs = %v", n)
	}
}
