package core

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/ports"
)

func TestArrangeLayers(t *testing.T) {
	bar := ports.LayerSurface{ID: 1, Layer: ports.LayerTop, Anchor: ports.AnchorTop | ports.AnchorLeft | ports.AnchorRight, Width: 1920, Height: 30, ExclusiveZone: 30}
	tests := []struct {
		name   string
		layers []ports.LayerSurface
		rects  []ports.Rect
		usable ports.Rect
	}{
		{"top", []ports.LayerSurface{bar}, []ports.Rect{{W: 1920, H: 30}}, ports.Rect{Y: 30, W: 1920, H: 1050}},
		{"bottom margin", []ports.LayerSurface{{Anchor: ports.AnchorBottom, Width: 100, Height: 20, ExclusiveZone: 20, Margin: [4]int32{0, 0, 5, 0}}}, []ports.Rect{{X: 910, Y: 1055, W: 100, H: 20}}, ports.Rect{W: 1920, H: 1055}},
		{"left dock", []ports.LayerSurface{{Anchor: ports.AnchorLeft | ports.AnchorTop | ports.AnchorBottom, Width: 40, Height: 1080, ExclusiveZone: 40}}, []ports.Rect{{W: 40, H: 1080}}, ports.Rect{X: 40, W: 1880, H: 1080}},
		{"center", []ports.LayerSurface{{Width: 100, Height: 40, Layer: ports.LayerOverlay}}, []ports.Rect{{X: 910, Y: 520, W: 100, H: 40}}, ports.Rect{W: 1920, H: 1080}},
		{"opposing horizontal anchors with margins", []ports.LayerSurface{{Anchor: ports.AnchorLeft | ports.AnchorRight, Width: 100, Height: 20, Margin: [4]int32{0, 30, 0, 10}}}, []ports.Rect{{X: 900, Y: 530, W: 100, H: 20}}, ports.Rect{W: 1920, H: 1080}},
		{"top only", []ports.LayerSurface{{Anchor: ports.AnchorTop, Width: 100, Height: 20, Margin: [4]int32{7, 0, 0, 0}}}, []ports.Rect{{X: 910, Y: 7, W: 100, H: 20}}, ports.Rect{W: 1920, H: 1080}},
		{"full span between margins", []ports.LayerSurface{{Anchor: ports.AnchorLeft | ports.AnchorRight | ports.AnchorTop, Height: 20, Margin: [4]int32{7, 30, 0, 10}}}, []ports.Rect{{X: 10, Y: 7, W: 1880, H: 20}}, ports.Rect{W: 1920, H: 1080}},
		{"opposing vertical anchors with margins", []ports.LayerSurface{{Anchor: ports.AnchorTop | ports.AnchorBottom, Width: 100, Height: 20, Margin: [4]int32{10, 0, 30, 0}}}, []ports.Rect{{X: 910, Y: 520, W: 100, H: 20}}, ports.Rect{W: 1920, H: 1080}},
		{"minus one", []ports.LayerSurface{bar, {Layer: ports.LayerTop, Anchor: ports.AnchorTop, Width: 100, Height: 20, ExclusiveZone: -1}}, []ports.Rect{{W: 1920, H: 30}, {X: 910, W: 100, H: 20}}, ports.Rect{Y: 30, W: 1920, H: 1050}},
		{"stack", []ports.LayerSurface{bar, {Layer: ports.LayerTop, Anchor: bar.Anchor, Width: 1920, Height: 20, ExclusiveZone: 20}}, []ports.Rect{{W: 1920, H: 30}, {Y: 30, W: 1920, H: 20}}, ports.Rect{Y: 50, W: 1920, H: 1030}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, u := arrangeLayers(1920, 1080, tt.layers)
			if u != tt.usable || len(got) != len(tt.rects) {
				t.Fatalf("got %v %v", got, u)
			}
			for i, r := range tt.rects {
				if got[i].Rect != r {
					t.Fatalf("rect %d: %v want %v", i, got[i].Rect, r)
				}
			}
		})
	}
	layers := []ports.LayerSurface{{ID: 1, Layer: ports.LayerOverlay}, {ID: 2, Layer: ports.LayerBottom}, {ID: 3, Layer: ports.LayerBackground}, {ID: 4, Layer: ports.LayerTop}, {ID: 5, Layer: ports.LayerBottom}}
	got, _ := arrangeLayers(100, 100, layers)
	for i, id := range []ports.WindowID{3, 2, 5, 4, 1} {
		if got[i].ID != id {
			t.Fatalf("order: %v", got)
		}
	}
}

func TestLayerChangedChannels(t *testing.T) {
	client := make(chan ports.ClientEvent)
	output := make(chan ports.OutputEvent)
	commands := make(chan ports.ClientCommand, 16)
	scenes := make(chan []ports.Scene, 1)
	cfg := ports.Config{}
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 2
	c, err := New(cfg, Channels{Client: client, Output: output, Commands: commands, Scenes: scenes})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	recv := func() ports.Scene {
		t.Helper()
		select {
		case s := <-scenes:
			return s[0]
		case <-time.After(time.Second):
			t.Fatal("scene timeout")
			return ports.Scene{}
		}
	}
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 1920, Height: 1080}}
	recv()
	client <- ports.WindowMapped{ID: 1}
	before := recv()
	for len(commands) > 0 {
		<-commands
	}
	bar := ports.LayerSurface{ID: 2, Layer: ports.LayerTop, Anchor: ports.AnchorTop | ports.AnchorLeft | ports.AnchorRight, Width: 1920, Height: 30, ExclusiveZone: 30}
	client <- ports.LayerChanged{Layers: []ports.LayerSurface{bar}}
	scene := recv()
	if len(scene.Layers) != 1 || scene.Layers[0].Rect != (ports.Rect{W: 1920, H: 30}) || len(scene.Windows) != 1 || scene.Windows[0].Rect.Y < 30 {
		t.Fatalf("scene: %v", scene)
	}
	if scene.Windows[0].Rect.H != before.Windows[0].Rect.H-30 {
		t.Fatalf("height: %v vs %v", scene.Windows, before.Windows)
	}
	select {
	case v := <-commands:
		cfg, ok := v.(ports.ConfigureWindow)
		if !ok || cfg.ID != 1 || cfg.Height != scene.Windows[0].Rect.H {
			t.Fatalf("configure: %v", v)
		}
	case <-time.After(time.Second):
		t.Fatal("configure timeout")
	}
	scene.Layers[0].Rect.Y = 99
	client <- ports.LayerChanged{Layers: []ports.LayerSurface{bar}}
	if next := recv(); next.Layers[0].Rect.Y != 0 {
		t.Fatalf("scene alias: %v", next)
	}
	cancel()
	<-done
}
