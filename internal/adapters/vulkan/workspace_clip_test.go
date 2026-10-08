package vulkan

import (
	"image"
	"image/color"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// A tile is cut at TileClip, so a bottom layer panel under it stays
// visible; a float there is drawn over it.
func TestTileClipKeepsBottomPanel(t *testing.T) {
	r, err := New(64, 48)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	red := color.RGBA{255, 0, 0, 255}
	green := color.RGBA{0, 255, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}
	contents := map[ports.WindowID]ports.SurfaceContent{1: solidContent(t, 64, 48, red), 2: solidContent(t, 64, 8, green), 3: solidContent(t, 8, 8, blue)}
	scene := ports.Scene{Seq: 1, OutputWidth: 64, OutputHeight: 48, Background: "#000000", TileClip: ports.Rect{W: 64, H: 40},
		Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 48}}},
		Layers:  []ports.SceneLayer{{ID: 2, Rect: ports.Rect{Y: 40, W: 64, H: 8}, Layer: ports.LayerBottom}},
	}
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	pixels := readPixels(t, r)
	for point, want := range map[image.Point]color.RGBA{{10, 10}: red, {10, 39}: red, {10, 40}: green, {60, 47}: green} {
		if got := pixels.RGBAAt(point.X, point.Y); got != want {
			t.Errorf("pixel %v=%v, want %v", point, got, want)
		}
	}
	scene.Seq++
	scene.Windows = []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 48}}, {ID: 3, Rect: ports.Rect{Y: 40, W: 8, H: 8}, Floating: true}}
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	if got := readPixels(t, r).RGBAAt(2, 44); got != blue {
		t.Fatalf("float clipped at the panel: %v", got)
	}
}

func TestWorkspaceClipWindowsPopupsAndLayers(t *testing.T) {
	r, err := New(64, 48)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	red := color.RGBA{255, 0, 0, 255}
	green := color.RGBA{0, 255, 0, 255}
	contents := map[ports.WindowID]ports.SurfaceContent{1: solidContent(t, 64, 48, red), 2: solidContent(t, 8, 8, green)}
	clip := ports.Rect{X: 16, Y: 12, W: 32, H: 24}
	scene := ports.Scene{Seq: 1, OutputWidth: 64, OutputHeight: 48, Background: "#000000", WorkspaceClip: clip,
		Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 48}}},
		Layers:  []ports.SceneLayer{{ID: 2, Rect: ports.Rect{W: 8, H: 8}, Layer: ports.LayerTop}},
	}
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	pixels := readPixels(t, r)
	for point, want := range map[image.Point]color.RGBA{{0, 0}: green, {10, 10}: {0, 0, 0, 255}, {16, 12}: red, {47, 35}: red, {48, 36}: {0, 0, 0, 255}} {
		if got := pixels.RGBAAt(point.X, point.Y); got != want {
			t.Errorf("pixel %v=%v, want %v", point, got, want)
		}
	}
	// Content-only damage at the same scene sequence must remain clipped.
	changed := contents[1]
	changed.Seq++
	changed.DamageHistory = []ports.SeqDamage{{Seq: changed.Seq, Full: true}}
	if _, err := changed.SHM.File.WriteAt([]byte{255, 255, 255, 255}, 0); err != nil {
		t.Fatal(err)
	}
	contents[1] = changed
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	if got := readPixels(t, r).RGBAAt(10, 10); got != (color.RGBA{0, 0, 0, 255}) {
		t.Fatalf("damage escaped clip: %v", got)
	}
	// A popup from a workspace surface is clipped too; input popups over
	// layers retain output-wide bounds.
	scene.Seq++
	scene.Windows = []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 48}, Popup: true}}
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	if got := readPixels(t, r).RGBAAt(10, 10); got != (color.RGBA{0, 0, 0, 255}) {
		t.Fatalf("popup escaped clip: %v", got)
	}
	// Popups above layers belong to output-wide controls, not the workspace.
	// Scenes are immutable snapshots (the target holds the previous one):
	// a changed window goes in a fresh slice, as core publishes it.
	scene.Seq++
	scene.Windows = []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 48}, Popup: true, OverLayers: true}}
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	if got := readPixels(t, r).RGBAAt(10, 10); got != red {
		t.Fatalf("output-wide popup clipped: %v", got)
	}
	scene.Windows = []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 48}, Popup: true}}
	// Switching back to inherited geometry repaints the margins.
	scene.Seq++
	scene.WorkspaceClip = ports.Rect{}
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	if got := readPixels(t, r).RGBAAt(10, 10); got != red {
		t.Fatalf("stale clip after inheritance: %v", got)
	}
}
