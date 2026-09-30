package vulkan

import (
	"fmt"
	"image/color"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// BenchmarkCaptureSessionFrame compares what a frame costs (CPU recording plus
// GPU completion, waited on the CPU) with no capture, one standard capture, and
// a clean capture plus the displayed frame (the second composition of the
// exclusion path). Scenes have Seq 0: every frame is a full redraw, as when
// the scene changes each frame.
//
//	go test ./internal/adapters/vulkan -run '^$' -bench CaptureSessionFrame -benchmem
func BenchmarkCaptureSessionFrame(b *testing.B) {
	for _, size := range [][2]int{{320, 240}, {1920, 1080}} {
		w, h := size[0], size[1]
		r, err := New(w, h)
		if err != nil {
			b.Skipf("Vulkan unavailable: %v", err)
		}
		if !r.syncFD || !r.dd.HasGetSemaphoreFdKHR() {
			r.Close()
			b.Skip("Vulkan sync-file export unavailable")
		}
		win := solidContent(&testing.T{}, 64, 64, color.RGBA{G: 200, A: 255})
		hud := solidContent(&testing.T{}, 64, 16, color.RGBA{B: 200, A: 255})
		win.ID, win.Surface, win.Seq, win.Version = 1, 1, 1, 1
		hud.ID, hud.Surface, hud.Seq, hud.Version = 10, 2, 1, 1
		contents := map[ports.WindowID]ports.SurfaceContent{1: win, 10: hud}
		shown := ports.Scene{
			Scale: 1, Background: "#101010",
			Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: w, H: h}}},
			Layers:  []ports.SceneLayer{{ID: 10, Layer: ports.LayerTop, Rect: ports.Rect{X: 8, Y: 8, W: 64, H: 16}}},
			Capture: &ports.SceneCapture{Session: 1, TargetRect: ports.Rect{W: w, H: h}, BorderWidth: 2, BorderColor: ports.CaptureBorderColor, Excluded: []ports.WindowID{10}},
		}
		clean := shown
		clean.Capture, clean.Layers = nil, nil
		frame := func(b *testing.B, s ports.Scene, capture bool) {
			if err := render(r, s, contents); err != nil {
				b.Fatal(err)
			}
			if capture {
				cf, err := r.BeginCapture()
				if err != nil {
					b.Fatal(err)
				}
				defer r.EndCapture(cf)
			}
		}
		wait := func(b *testing.B) {
			if err := r.waitFrame(r.submitted); err != nil {
				b.Fatal(err)
			}
			for _, s := range r.captures {
				if s.pending {
					if err := checked("wait", r.dd.WaitForFences(r.device, 1, &s.fence, 1, 1<<62)); err != nil {
						b.Fatal(err)
					}
				}
			}
		}
		for _, tc := range []struct {
			name string
			run  func(b *testing.B)
		}{
			{"render", func(b *testing.B) { frame(b, shown, false) }},
			{"render+capture", func(b *testing.B) { frame(b, shown, true) }},
			{"clean+capture+render", func(b *testing.B) { frame(b, clean, true); frame(b, shown, false) }},
			{"clean+capture+render+capture", func(b *testing.B) { frame(b, clean, true); frame(b, shown, true) }},
		} {
			b.Run(fmt.Sprintf("%dx%d/%s", w, h, tc.name), func(b *testing.B) {
				tc.run(b)
				wait(b)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					tc.run(b)
					wait(b)
				}
			})
		}
		r.Close()
	}
}
