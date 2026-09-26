package vulkan

import (
	"image/color"
	"testing"

	"github.com/bnema/nefertty/internal/ports"
)

// Frames drawn into exported targets reach their dmabufs: another renderer
// importing a target (as KMS scans it out) sees the frame, and readback
// reads the target drawn last.
func TestExportTargets(t *testing.T) {
	r, err := New(64, 32)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	bufs, err := r.ExportTargets(2, nil)
	if err != nil {
		t.Skipf("no exportable targets: %v", err)
	}
	defer func() {
		for _, b := range bufs {
			b.Planes[0].File.Close()
		}
	}()
	if len(bufs) != 2 || bufs[0].Width != 64 || bufs[0].Format != fourccXRGB || bufs[0].Planes[0].Stride < 64*4 {
		t.Fatalf("targets %+v", bufs)
	}
	colors := []string{"#ff0000", "#0000ff"}
	for i, c := range colors {
		r.UseTarget(i)
		if _, err := r.Render(ports.Scene{Background: c}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.Pixels().At(3, 3); got != (color.RGBA{0, 0, 255, 255}) {
		t.Fatalf("readback of the last target %v", got)
	}
	// Import target 0 as a client buffer, like the display would.
	viewer, err := New(64, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()
	b := bufs[0]
	b.ID = 1
	importable := false
	for _, f := range viewer.DMABuf().Formats {
		importable = importable || f.Format == b.Format && f.Modifier == b.Modifier
	}
	if !importable {
		t.Skipf("modifier %#x not importable", b.Modifier)
	}
	scene := ports.Scene{Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 32}}}}
	contents := map[ports.WindowID]ports.SurfaceContent{1: {ID: 1, Width: 64, Height: 32, Opaque: true, DMABuf: &b}}
	if _, err := viewer.Render(scene, contents); err != nil {
		t.Fatal(err)
	}
	if got := viewer.Pixels().At(10, 10); got != (color.RGBA{255, 0, 0, 255}) {
		t.Fatalf("target 0 as seen by the display %v", got)
	}
}
