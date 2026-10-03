package core

import (
	"context"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestCoreCoveringFloatSceneAndHit(t *testing.T) {
	cfg := ports.Config{}
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 2
	cfg.Border.Width = 2
	cfg.Floating.Dim = 0.3
	scenes := make(chan []ports.Scene, 1)
	commands := make(chan ports.ClientCommand, 128)
	c, err := New(cfg, Channels{Scenes: scenes, Commands: commands})
	if err != nil {
		t.Fatal(err)
	}
	c.cur().info = ports.OutputInfo{Name: "OUT", Width: 100, Height: 80}
	m := c.cur().mon
	m.SetOutput(100, 80)
	w := m.Current()
	w.AddWindow(1)
	w.AddFloating(2, 100, 80)
	ctx := context.Background()
	check := func(dim float64, ids ...WindowID) {
		t.Helper()
		if err := c.publish(ctx); err != nil {
			t.Fatal(err)
		}
		s := (<-scenes)[0]
		if s.Dim != dim {
			t.Fatalf("dim %v, want %v", s.Dim, dim)
		}
		if len(s.Windows) != len(ids) {
			t.Fatalf("scene %+v", s.Windows)
		}
		for i, id := range ids {
			if s.Windows[i].ID != id {
				t.Fatalf("scene %+v, want %v", s.Windows, ids)
			}
		}
	}
	check(0.3, 1, 2)
	w.FocusID(1)
	check(0, 2, 1)
	if !w.Layout()[0].Below {
		t.Fatal("float not below")
	}
	if id, _, _ := c.hit(50, 40); id != 1 {
		t.Fatalf("hit %d, want tile", id)
	}
	w.AddFloating(3, 12, 10)
	check(0.3, 2, 1, 3)
	if id, _, _ := c.hit(50, 40); id != 3 {
		t.Fatalf("hit %d, want dialog", id)
	}
	w.FocusID(2)
	check(0.3, 1, 2, 3)
	m.ToggleOverview()
	if err := c.publish(ctx); err != nil {
		t.Fatal(err)
	}
	if s := (<-scenes)[0]; s.Dim != 0.3 || !s.DimBehind {
		t.Fatalf("overview dim %v behind %v", s.Dim, s.DimBehind)
	}
}

// The overview darkens the wallpaper behind its previews, tiles only too.
func TestCoreOverviewDimsBehind(t *testing.T) {
	cfg := ports.Config{}
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 2
	cfg.Floating.Dim = 0.3
	scenes := make(chan []ports.Scene, 1)
	c, err := New(cfg, Channels{Scenes: scenes, Commands: make(chan ports.ClientCommand, 128)})
	if err != nil {
		t.Fatal(err)
	}
	c.cur().info = ports.OutputInfo{Name: "OUT", Width: 100, Height: 80}
	m := c.cur().mon
	m.SetOutput(100, 80)
	m.Current().AddWindow(1)
	ctx := context.Background()
	for _, tc := range []struct {
		dim    float64
		behind bool
	}{{0, false}, {0.3, true}, {0, false}} {
		if err := c.publish(ctx); err != nil {
			t.Fatal(err)
		}
		if s := (<-scenes)[0]; s.Dim != tc.dim || s.DimBehind != tc.behind {
			t.Fatalf("dim %v behind %v, want %v %v", s.Dim, s.DimBehind, tc.dim, tc.behind)
		}
		m.ToggleOverview()
	}
}
