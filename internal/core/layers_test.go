package core

import (
	"context"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

// laterClock moves fullscreenGrace forward on each Now: fullscreen
// requests are past the grace.
func laterClock(t *testing.T) *portsmocks.MockClock {
	t.Helper()
	now := time.Unix(0, 0)
	clock := portsmocks.NewMockClock(t)
	clock.EXPECT().Now().RunAndReturn(func() time.Time {
		now = now.Add(fullscreenGrace)
		return now
	}).Maybe()
	return clock
}

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

// In scroll overflow, a fullscreen column the user scrolled off no longer
// covers the output: the layers show again.
func TestScrolledOffFullscreenShowsLayers(t *testing.T) {
	m := newMonitor("", "")
	m.SetOutput(100, 80)
	m.AddWindow(1)
	m.SetFullscreen(1, true)
	m.AddWindow(2)
	sc := &screen{mon: m}
	bar := ports.SceneLayer{ID: 6, Layer: ports.LayerTop}
	if shown(sc, bar) {
		t.Fatal("bar shown over fullscreen")
	}
	m.Current().FocusID(2)
	if !shown(sc, bar) {
		t.Fatal("bar hidden with the fullscreen column scrolled off")
	}
}

// A fullscreen window is exclusive: the scene carries only the background
// and a layer taking the keyboard exclusively (a locker), so the output
// can scan the window out.
func TestFullscreenSceneLayers(t *testing.T) {
	client := make(chan ports.ClientEvent)
	output := make(chan ports.OutputEvent)
	commands := make(chan ports.ClientCommand, 64)
	scenes := make(chan []ports.Scene, 1)
	cfg := ports.Config{}
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 2
	c, err := New(cfg, Channels{Client: client, Output: output, Commands: commands, Scenes: scenes, Clock: laterClock(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	go func() {
		for range commands {
		}
	}()
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
	layers := func(s ports.Scene) []ports.WindowID {
		var ids []ports.WindowID
		for _, l := range s.Layers {
			ids = append(ids, l.ID)
		}
		return ids
	}
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	recv()
	client <- ports.WindowMapped{ID: 1}
	recv()
	all := []ports.LayerSurface{
		{ID: 5, Layer: ports.LayerBackground, Anchor: 15},
		{ID: 6, Layer: ports.LayerTop, Anchor: ports.AnchorTop | ports.AnchorLeft | ports.AnchorRight, Height: 10},
		{ID: 7, Layer: ports.LayerOverlay, Width: 20, Height: 10},
	}
	client <- ports.LayerChanged{Layers: all}
	if got := layers(recv()); len(got) != 3 {
		t.Fatalf("layers %v", got)
	}
	client <- ports.WindowFullscreenRequest{ID: 1, Fullscreen: true}
	if got := layers(recv()); len(got) != 1 || got[0] != 5 {
		t.Fatalf("fullscreen layers %v, want the background", got)
	}
	locker := ports.LayerSurface{ID: 8, Layer: ports.LayerOverlay, Anchor: 15, Keyboard: 1}
	client <- ports.LayerChanged{Layers: append(all, locker)}
	if got := layers(recv()); len(got) != 2 || got[1] != 8 {
		t.Fatalf("fullscreen layers %v, want the background and the locker", got)
	}
	cancel()
	<-done
	close(commands)
}
