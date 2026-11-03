package core

import (
	"image"
	"testing"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/domain"
)

func TestIsolatedOutputPlacementAndPointer(t *testing.T) {
	c := layoutCore(1, tile{"tv", 0, 0, 3840, 2160}, tile{"DP-1", 0, 0, 5120, 1440})
	c.screens[0].info.Virtual = true
	c.cfg.Outputs = []domain.OutputConfig{{Name: "tv", Virtual: true, Primary: true}}
	c.order()
	if c.screens[0].x != 5120+2024 || c.screens[0].y != 0 || c.screens[1].x != 0 || c.focusScreen != 1 {
		t.Fatalf("origins/focus: %+v %+v %d", c.screens[0], c.screens[1], c.focusScreen)
	}
	if !c.layout()[0].Isolated || c.layout()[0].Primary {
		t.Fatal(c.layout())
	}
	if c.screenAt(7200, 100) != -1 || c.outputAt(7200, 100) != 0 {
		t.Fatal("pointer/client lookup conflated")
	}
	if x, y := c.clampPointer(100, 100, 7200, 100); x != 5119 || y != 100 {
		t.Fatalf("pointer %v %v", x, y)
	}
	if c.neighbor(dirRight) != -1 {
		t.Fatal("isolated neighbor reachable")
	}
	for _, mode := range []domain.NavEdge{domain.NavEdgeScreen, domain.NavEdgeWorkspace} {
		c.cfg.Navigation.Edge = mode
		if hop := c.edgeTarget(layout.Horizontal, 1, dirRight); hop.kind == edgeScreen {
			t.Fatal("edge navigation reaches isolated output")
		}
	}
	c.moveWorkspace(dirRight)
	if c.focusScreen != 1 {
		t.Fatal("workspace moved to isolated screen")
	}
	c.cfg.Outputs = append(c.cfg.Outputs, domain.OutputConfig{Name: "DP-1", Pos: &image.Point{}})
	c.cfg.Outputs[0].Pos = &image.Point{X: 5120}
	c.order()
	if c.screens[0].isolated || c.screenAt(5200, 100) != 0 || c.neighbor(dirRight) != 0 {
		t.Fatal("placed virtual unreachable")
	}
	c.cfg.Outputs[0].Pos = nil
	c.cfg.Outputs[0].Anchor = domain.OutputAnchor{To: "DP-1", Relation: domain.RelationRightOf}
	c.order()
	if c.screens[0].isolated || c.screens[0].x != 5120 {
		t.Fatal("anchored virtual isolated")
	}
}

func TestMultipleIsolatedOutputsDoNotOverlap(t *testing.T) {
	positions := placeOutputs([]placeItem{{name: "tv", w: 1280, h: 720, isolated: true}, {name: "DP-1", w: 640, h: 360}, {name: "tablet", w: 800, h: 600, isolated: true}})
	if positions[1].X != 0 || positions[0].X != 640+2024 || positions[2].X != positions[0].X || positions[2].Y != 720+2024 {
		t.Fatal(positions)
	}
}

func TestLargeIsolatedColumnWithNegativeOrigin(t *testing.T) {
	items := []placeItem{{name: "DP-1", w: 5120, h: 1440, pos: &image.Point{X: -5120, Y: -100}}, {name: "a", w: 16384, h: 16384, isolated: true}, {name: "b", w: 16384, h: 16384, isolated: true}}
	p := placeOutputs(items)
	if p[1] != image.Pt(2024, -100) || p[2] != image.Pt(2024, 18308) {
		t.Fatal(p)
	}
	if width := p[2].X + items[2].w - p[0].X; width != 23528 || width > 32767 {
		t.Fatal(width)
	}
}

func TestIsolatedFocusSurvivesResizeAndReload(t *testing.T) {
	c := layoutCore(0, tile{"DP-1", 0, 0, 640, 360}, tile{"tv", 2664, 0, 1280, 720})
	c.screens[1].info.Virtual = true
	c.screens[1].scale = 1
	c.cfg.Outputs = []domain.OutputConfig{{Name: "tv", Virtual: true, Mode: "1280x720"}}
	c.order()
	c.focusScreen = c.screenIndex("tv") // An explicit window/output activation.
	c.addScreen(domain.OutputInfo{Name: "tv", Virtual: true, Width: 1920, Height: 1080})
	if c.cur().name() != "tv" {
		t.Fatal("resize stole focus")
	}
	c.cfg.Outputs = []domain.OutputConfig{{Name: "DP-1"}, {Name: "tv", Virtual: true, Mode: "1920x1080", Scale: 1.5}}
	c.applyOutputConfig()
	if c.cur().name() != "tv" {
		t.Fatal("reload stole focus")
	}
}

func TestStartupIsolatedFocusFallsBackWhenReachableOutputArrives(t *testing.T) {
	c := layoutCore(0, tile{"tv", 0, 0, 1280, 720})
	c.screens[0].info.Virtual = true
	c.order()
	c.addScreen(domain.OutputInfo{Name: "DP-1", Width: 640, Height: 360})
	if c.cur().name() != "DP-1" {
		t.Fatal("startup chose isolated output")
	}
}
