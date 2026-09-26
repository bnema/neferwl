package core_test

import (
	"context"
	"testing"

	"github.com/bnema/nefertty/internal/adapters/config"
	"github.com/bnema/nefertty/internal/core"
	"github.com/bnema/nefertty/internal/ports"
)

// next returns the next command of type T matching ok, skipping others.
func next[T ports.ClientCommand](t *testing.T, ch <-chan ports.ClientCommand, ok func(T) bool) T {
	t.Helper()
	for {
		if v, is := command(t, ch).(T); is && ok(v) {
			return v
		}
	}
}

func anyOf[T any](T) bool { return true }

// Bars and launchers get the pointer, popups and, on click, the keyboard.
func TestLayerPointerPopupAndFocus(t *testing.T) {
	cfg := config.Defaults()
	cfg.Border.Width = 0
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 256)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	scene(t, scenes)
	if f := next(t, commands, anyOf[ports.FocusWindow]); f.ID != 1 {
		t.Fatalf("focus %d", f.ID)
	}
	bar := ports.LayerSurface{ID: 6, Layer: ports.LayerTop, Anchor: ports.AnchorTop | ports.AnchorLeft | ports.AnchorRight, Width: 100, Height: 10, ExclusiveZone: 10, Keyboard: 2}
	client <- ports.LayerChanged{Layers: []ports.LayerSurface{bar}}
	scene(t, scenes)

	// The bar gets the pointer, in its own coordinates.
	input <- ports.PointerMotion{X: 50, Y: 5}
	if v := next(t, commands, anyOf[ports.PointerFocus]); v.ID != 6 || v.X != 50 || v.Y != 5 {
		t.Fatalf("pointer focus %+v, want the bar", v)
	}
	// A click gives the on-demand bar the keyboard.
	input <- ports.PointerButton{Button: 0x110, Pressed: true}
	if v := next(t, commands, anyOf[ports.PointerButtonTo]); v.ID != 6 {
		t.Fatalf("button to %d", v.ID)
	}
	if f := next(t, commands, anyOf[ports.FocusWindow]); f.ID != 6 {
		t.Fatalf("focus %d, want the bar", f.ID)
	}
	input <- ports.PointerButton{Button: 0x110}

	// A menu from the bar is placed under it and slides inside the output.
	pos := ports.Positioner{Width: 30, Height: 20, AnchorRect: ports.Rect{X: 90, Y: 0, W: 10, H: 10}, Anchor: ports.EdgeBottomLeft, Gravity: ports.EdgeBottomRight, Adjust: ports.AdjustSlideX}
	client <- ports.PopupRequest{ID: 9, Parent: 6, Positioner: pos, Grab: true}
	cp := next(t, commands, func(v ports.ConfigurePopup) bool { return v.ID == 9 })
	if cp.Rect != (ports.Rect{X: 70, Y: 10, W: 30, H: 20}) {
		t.Fatalf("popup rect %+v", cp.Rect)
	}
	client <- ports.PopupMapped{ID: 9}
	sceneMatch(t, scenes, func(sc ports.Scene) bool {
		w := sc.Windows[len(sc.Windows)-1]
		return w.Popup && w.ID == 9 && w.Rect == (ports.Rect{X: 70, Y: 10, W: 30, H: 20})
	})
	if f := next(t, commands, anyOf[ports.FocusWindow]); f.ID != 9 {
		t.Fatalf("grab focus %d", f.ID)
	}
	input <- ports.PointerMotion{X: 80, Y: 15}
	if v := next(t, commands, anyOf[ports.PointerFocus]); v.ID != 9 || v.X != 10 || v.Y != 5 {
		t.Fatalf("pointer focus %+v, want the menu", v)
	}

	// A click on the window closes the menu and takes the keyboard back.
	input <- ports.PointerMotion{X: 20, Y: 50}
	if v := next(t, commands, anyOf[ports.PointerFocus]); v.ID != 1 {
		t.Fatalf("pointer focus %+v, want the window", v)
	}
	input <- ports.PointerButton{Button: 0x110, Pressed: true}
	if v := next(t, commands, anyOf[ports.ClosePopup]); v.ID != 9 {
		t.Fatalf("closed %d", v.ID)
	}
	if f := next(t, commands, anyOf[ports.FocusWindow]); f.ID != 1 {
		t.Fatalf("focus %d, want the window", f.ID)
	}
	input <- ports.PointerButton{Button: 0x110}

	// An exclusive launcher wins over a clicked on-demand bar.
	input <- ports.PointerMotion{X: 50, Y: 5}
	next(t, commands, func(v ports.PointerFocus) bool { return v.ID == 6 })
	input <- ports.PointerButton{Button: 0x110, Pressed: true}
	next(t, commands, func(v ports.FocusWindow) bool { return v.ID == 6 })
	input <- ports.PointerButton{Button: 0x110}
	launcher := ports.LayerSurface{ID: 7, Layer: ports.LayerOverlay, Width: 20, Height: 20, Keyboard: 1}
	client <- ports.LayerChanged{Layers: []ports.LayerSurface{bar, launcher}}
	if f := next(t, commands, anyOf[ports.FocusWindow]); f.ID != 7 {
		t.Fatalf("focus %d, want the launcher", f.ID)
	}
	client <- ports.LayerChanged{Layers: []ports.LayerSurface{bar}}
	if f := next(t, commands, anyOf[ports.FocusWindow]); f.ID != 6 {
		t.Fatalf("focus %d, want the bar back", f.ID)
	}

	// Unmapped then mapped again, the bar does not take the keyboard back.
	client <- ports.LayerChanged{}
	next(t, commands, func(v ports.FocusWindow) bool { return v.ID == 1 })
	client <- ports.LayerChanged{Layers: []ports.LayerSurface{bar}}
	scene(t, scenes)
	input <- ports.PointerMotion{X: 20, Y: 50} // publishes
	next(t, commands, func(v ports.PointerFocus) bool { return v.ID == 1 })
	select {
	case v := <-commands:
		if f, ok := v.(ports.FocusWindow); ok {
			t.Fatalf("focus moved to %d", f.ID)
		}
	default:
	}

	// A window focus change by keyboard or a new window takes it back too.
	input <- ports.PointerMotion{X: 50, Y: 5}
	next(t, commands, func(v ports.PointerFocus) bool { return v.ID == 6 })
	input <- ports.PointerButton{Button: 0x110, Pressed: true}
	next(t, commands, func(v ports.FocusWindow) bool { return v.ID == 6 })
	input <- ports.PointerButton{Button: 0x110}
	client <- ports.WindowMapped{ID: 2}
	if f := next(t, commands, anyOf[ports.FocusWindow]); f.ID != 2 {
		t.Fatalf("focus %d, want the new window", f.ID)
	}
}

// A window's menu stays with the windows: an overlay layer covers it.
func TestWindowPopupUnderOverlay(t *testing.T) {
	cfg := config.Defaults()
	cfg.Border.Width = 0
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 256)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	scene(t, scenes)
	client <- ports.PopupRequest{ID: 9, Parent: 1, Positioner: ports.Positioner{Width: 30, Height: 20, AnchorRect: ports.Rect{X: 0, Y: 0, W: 10, H: 10}}}
	next(t, commands, func(v ports.ConfigurePopup) bool { return v.ID == 9 })
	client <- ports.PopupMapped{ID: 9}
	sc := sceneMatch(t, scenes, func(sc ports.Scene) bool { return len(sc.Windows) == 2 })
	if w := sc.Windows[1]; !w.Popup || w.OverLayers {
		t.Fatalf("window popup %+v", w)
	}
	osd := ports.LayerSurface{ID: 7, Layer: ports.LayerOverlay, Anchor: 15}
	client <- ports.LayerChanged{Layers: []ports.LayerSurface{osd}}
	scene(t, scenes)
	input <- ports.PointerMotion{X: 5, Y: 5}
	if v := next(t, commands, anyOf[ports.PointerMotionTo]); v.ID != 7 {
		t.Fatalf("hit %d, want the overlay", v.ID)
	}
}

// A fullscreen window hides top and bottom layers from the pointer, not
// overlay; background and bottom layers get it only where no window is.
func TestLayerHitOrder(t *testing.T) {
	cfg := config.Defaults()
	cfg.Border.Width = 0
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 256)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, scenes)
	wallpaper := ports.LayerSurface{ID: 5, Layer: ports.LayerBackground, Anchor: 15}
	bar := ports.LayerSurface{ID: 6, Layer: ports.LayerTop, Anchor: ports.AnchorTop | ports.AnchorLeft | ports.AnchorRight, Height: 10}
	osd := ports.LayerSurface{ID: 7, Layer: ports.LayerOverlay, Anchor: ports.AnchorBottom, Width: 20, Height: 10}
	client <- ports.LayerChanged{Layers: []ports.LayerSurface{wallpaper, bar, osd}}
	scene(t, scenes)
	hit := func(x, y float64, want ports.WindowID) {
		t.Helper()
		input <- ports.PointerMotion{X: x, Y: y}
		// A focus change is sent only when the target changes; motion always.
		if v := next(t, commands, anyOf[ports.PointerMotionTo]); v.ID != want {
			t.Fatalf("(%v,%v) hit %d, want %d", x, y, v.ID, want)
		}
	}
	hit(50, 40, 5)
	hit(50, 5, 6)
	client <- ports.WindowMapped{ID: 1}
	scene(t, scenes)
	hit(50, 40, 1)
	hit(50, 5, 6)
	hit(50, 75, 7)
	client <- ports.WindowFullscreenRequest{ID: 1, Fullscreen: true}
	scene(t, scenes)
	hit(50, 5, 1)
	hit(50, 75, 7)
}

// The pointer moving to another output does not take the keyboard from a
// clicked on-demand layer: keys still reach it.
func TestLayerFocusAcrossOutputs(t *testing.T) {
	cfg := config.Defaults()
	cfg.Border.Width = 0
	client := make(chan ports.ClientEvent, 8)
	input := make(chan ports.InputEvent, 8)
	output := make(chan ports.OutputEvent, 8)
	commands := make(chan ports.ClientCommand, 256)
	scenes := make(chan []ports.Scene, 1)
	c, err := core.New(cfg, core.Channels{Client: client, Input: input, Output: output, Commands: commands, Scenes: scenes})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80}}
	scene(t, scenes)
	output <- ports.OutputAdded{Info: ports.OutputInfo{Name: "OUT-2", Width: 100, Height: 80}}
	scene(t, scenes)
	client <- ports.WindowMapped{ID: 1}
	scene(t, scenes)
	bar := ports.LayerSurface{ID: 6, Layer: ports.LayerTop, Anchor: ports.AnchorTop | ports.AnchorLeft | ports.AnchorRight, Height: 10, Keyboard: 2, Output: "OUT-1"}
	client <- ports.LayerChanged{Layers: []ports.LayerSurface{bar}}
	scene(t, scenes)
	input <- ports.PointerMotion{X: 50, Y: 5}
	next(t, commands, func(v ports.PointerFocus) bool { return v.ID == 6 })
	input <- ports.PointerButton{Button: 0x110, Pressed: true}
	next(t, commands, func(v ports.FocusWindow) bool { return v.ID == 6 })
	input <- ports.PointerButton{Button: 0x110}
	input <- ports.PointerMotion{X: 100, Y: 40} // onto OUT-2, now focused
	next(t, commands, func(v ports.PointerFocus) bool { return v.ID == 0 })
	input <- ports.KeyEvent{Keysym: "a", Pressed: true}
	if v := next(t, commands, anyOf[ports.ForwardKey]); v.ID != 6 {
		t.Fatalf("key to %d, want the bar", v.ID)
	}
}
