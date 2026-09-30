package core

import (
	"context"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// partlyShown has window 2 straddling the left edge of a 100x100 viewport:
// its client is x 75..125 of the monitor (1075..1125 globally) and only
// 100..125 shows.
func partlyShown(t *testing.T) *Core {
	t.Helper()
	var cfg ports.Config
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 2
	c, err := New(cfg, Channels{
		Scenes: make(chan []ports.Scene, 1), Layouts: make(chan ports.Layout, 1), Constraints: make(chan ports.PointerConstraint, 1),
		State: make(chan ports.State, 1), Workspaces: make(chan ports.Workspaces, 1), Commands: make(chan ports.ClientCommand, 16),
	})
	if err != nil {
		t.Fatal(err)
	}
	c.addScreen(ports.OutputInfo{Name: "A", Width: 300, Height: 200})
	s := c.screens[0]
	s.x = 1000
	s.mon.SetNamed([]NamedWorkspace{{Name: "s", Size: [2]int{100, 100}}})
	s.mon.ToggleNamed("s")
	for id := WindowID(1); id <= 3; id++ {
		s.mon.AddWindow(id)
	}
	s.mon.Current().ViewX += 25
	return c
}

// A constraint region is window-local: it starts at the whole client's
// origin, not at the visible part's, and is then cut to the viewport.
func TestConstraintRegionUsesFullClientOrigin(t *testing.T) {
	c := partlyShown(t)
	full, vis, ok := c.shownClient(2)
	if !ok || full != (Rect{X: 1075, Y: 50, W: 50, H: 100}) || vis != (Rect{X: 1100, Y: 50, W: 25, H: 100}) {
		t.Fatalf("full %+v visible %+v %v", full, vis, ok)
	}
	c.constrained = ports.PointerConstrained{ID: 2, PointerConstraint: ports.PointerConstraint{Mode: ports.ConstraintConfine, Rect: Rect{X: 30, Y: 10, W: 20, H: 20}}}
	c.resolveConstraint()
	// Local x 30..50 is global 1105..1125, all inside the visible part.
	if want := (Rect{X: 1105, Y: 60, W: 20, H: 20}); c.constraint.Rect != want {
		t.Fatalf("region %+v want %+v", c.constraint.Rect, want)
	}
	// A region straddling the viewport edge is cut to it: local 10..40 is 1085..1115.
	c.constrained.PointerConstraint.Rect = Rect{X: 10, Y: 0, W: 30, H: 100}
	c.resolveConstraint()
	if want := (Rect{X: 1100, Y: 50, W: 15, H: 100}); c.constraint.Rect != want {
		t.Fatalf("straddling region %+v want %+v", c.constraint.Rect, want)
	}
}

// A warp target is computed from the whole client origin and refused when it
// falls outside the viewport.
func TestWarpUsesFullClientOriginAndFrame(t *testing.T) {
	c := partlyShown(t)
	c.pointer = 2
	ctx := context.Background()
	// Local 10 is global 1085: the hidden part of the window.
	if err := c.warpPointer(ctx, ports.PointerWarp{ID: 2, X: 10, Y: 5}); err != nil {
		t.Fatal(err)
	}
	if c.cursorX != 0 || c.cursorY != 0 {
		t.Fatalf("warped into the hidden part: %v,%v", c.cursorX, c.cursorY)
	}
	// Local 30 is global 1105: shown.
	if err := c.warpPointer(ctx, ports.PointerWarp{ID: 2, X: 30, Y: 5}); err != nil {
		t.Fatal(err)
	}
	if c.cursorX != 1105 || c.cursorY != 55 {
		t.Fatalf("cursor %v,%v want 1105,55", c.cursorX, c.cursorY)
	}
}
