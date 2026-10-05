package vulkan

import (
	"fmt"
	"image/color"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// BenchmarkComposeFullTarget composes a full-redraw frame (three windows
// covering the output, moved every frame as a workspace swipe does) into an
// exported target of each modifier the device offers for B8G8R8A8, and waits
// for the GPU after every frame: the time per frame is the GPU cost of the
// target's tiling/compression layout (DCC retile passes included). Select the
// GPU with NEFERWL_VK_DEVICE; the size is 2560x1600.
func BenchmarkComposeFullTarget(b *testing.B) {
	const w, h = 2560, 1600
	probe, err := New(w, h)
	if err != nil {
		b.Skipf("Vulkan unavailable: %v", err)
	}
	mods := append([]uint64(nil), probe.renderMods...)
	planes := probe.modPlanes
	probe.Close()
	if len(mods) == 0 {
		b.Skip("device exports no render modifier")
	}
	for _, m := range mods {
		b.Run(fmt.Sprintf("%#x/planes=%d", m, planes[m]), func(b *testing.B) {
			r, err := New(w, h)
			if err != nil {
				b.Skipf("Vulkan unavailable: %v", err)
			}
			defer r.Close()
			bufs, err := r.ExportTargets(2, []uint64{m}, false)
			if err != nil {
				b.Skipf("modifier %#x not exportable: %v", m, err)
			}
			defer func() {
				for _, buf := range bufs {
					for _, p := range buf.Planes {
						p.File.Close()
					}
				}
			}()
			ww := w / 2
			contents := map[ports.WindowID]ports.SurfaceContent{}
			for i, c := range []color.RGBA{{R: 200, A: 255}, {G: 200, A: 255}, {B: 200, A: 255}} {
				content := solidContent(b, ww, h, c)
				content.ID, content.Surface, content.Seq, content.Version = ports.WindowID(i+1), uint64(i+1), 1, 1
				contents[content.ID] = content
			}
			scene := func(i int) ports.Scene {
				x := i % 64
				return ports.Scene{Seq: uint64(i + 1), Scale: 1, Background: "#101010", Windows: []ports.SceneWindow{
					{ID: 1, Rect: ports.Rect{X: x - ww, W: ww, H: h}},
					{ID: 2, Rect: ports.Rect{X: x, W: ww, H: h}},
					{ID: 3, Rect: ports.Rect{X: x + ww, W: ww, H: h}},
				}}
			}
			frame := func(i int) {
				r.UseTarget(i % 2)
				if err := render(r, scene(i), contents); err != nil {
					b.Fatal(err)
				}
				if err := r.waitFrame(r.submitted); err != nil {
					b.Fatal(err)
				}
			}
			for i := range 8 {
				frame(i)
			}
			b.ResetTimer()
			start := time.Now()
			for i := range b.N {
				frame(i + 8)
			}
			b.ReportMetric(float64(time.Since(start).Microseconds())/1000/float64(b.N), "ms/frame")
		})
	}
}
