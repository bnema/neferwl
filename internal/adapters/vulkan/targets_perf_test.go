package vulkan

import (
	"fmt"
	"image/color"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/domain"
)

// BenchmarkComposeFullTarget composes a full-redraw frame (three windows
// covering the output, moved every frame as a workspace swipe does) into an
// exported target of each modifier the device offers for B8G8R8A8, and waits
// for the GPU after every frame: the time per frame is the GPU cost of the
// target's tiling/compression layout (DCC retile passes included). Select the
// GPU with NEFERWL_RENDER_DEVICE; the size is 2560x1600.
func BenchmarkComposeFullTarget(b *testing.B) {
	const w, h = 2560, 1600
	probe := newTestRenderer(b, w, h)
	mods := append([]uint64(nil), probe.gpu.renderMods...)
	planes := probe.gpu.modPlanes
	probe.Close()
	if len(mods) == 0 {
		b.Skip("device exports no render modifier")
	}
	for _, m := range mods {
		b.Run(fmt.Sprintf("%#x/planes=%d", m, planes[m]), func(b *testing.B) {
			r := newTestRenderer(b, w, h)
			defer r.Close()
			bufs, err := r.ExportTargets(2, []uint64{m}, false)
			if err != nil {
				b.Skipf("modifier %#x not exportable: %v", m, err)
			}
			defer func() {
				for _, buf := range bufs {
					for _, p := range buf.Planes {
						_ = p.File.Close()
					}
				}
			}()
			ww := w / 2
			contents := map[domain.WindowID]domain.SurfaceContent{}
			for i, c := range []color.RGBA{{R: 200, A: 255}, {G: 200, A: 255}, {B: 200, A: 255}} {
				content := solidContent(b, ww, h, c)
				content.ID, content.Surface, content.Seq, content.Version = domain.WindowID(i+1), uint64(i+1), 1, 1
				contents[content.ID] = content
			}
			scene := func(i int) domain.Scene {
				x := i % 64
				return domain.Scene{Seq: uint64(i + 1), Scale: 1, Background: "#101010", Windows: []domain.SceneWindow{
					{ID: 1, Rect: domain.Rect{X: x - ww, W: ww, H: h}},
					{ID: 2, Rect: domain.Rect{X: x, W: ww, H: h}},
					{ID: 3, Rect: domain.Rect{X: x + ww, W: ww, H: h}},
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

// BenchmarkComposeHDRPartial measures a small changing SHM surface on alternating PQ targets.
func BenchmarkComposeHDRPartial(b *testing.B) {
	const w, h = 2560, 1600
	r := newTestRenderer(b, w, h)
	r.SetVirtualOutput(true)
	r.SetHDRReadback(true)
	r.SetHDR(203)
	bufs, err := r.ExportTargets(2, nil, false)
	if err != nil {
		b.Skipf("HDR targets unavailable: %v", err)
	}
	b.Cleanup(func() {
		for _, buf := range bufs {
			for _, p := range buf.Planes {
				_ = p.File.Close()
			}
		}
	})
	c := solidContent(b, 64, 64, color.RGBA{R: 200, A: 255})
	c.ID, c.Surface, c.Seq, c.Version = 1, 1, 1, 1
	contents := map[domain.WindowID]domain.SurfaceContent{1: c}
	scene := domain.Scene{Seq: 1, Scale: 1, Background: "#101010", Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 64, H: 64}}}}
	// Keep both intervening updates: each alternating target is two sequences old.
	c.DamageHistory = []domain.SeqDamage{{Seq: 1, Full: true}, {Seq: 1, Full: true}}
	frame := func(i int) {
		c.Seq++
		c.DamageHistory[0] = c.DamageHistory[1]
		c.DamageHistory[1] = domain.SeqDamage{Seq: c.Seq, Rects: []domain.Rect{{X: i % 32, W: 32, H: 64}}}
		if _, err := c.SHM.File.WriteAt([]byte{byte(i), 0, 200, 255}, int64(i%32*4)); err != nil {
			b.Fatal(err)
		}
		contents[1] = c
		r.UseTarget(i % 2)
		if err := render(r, scene, contents); err != nil {
			b.Fatal(err)
		}
		if err := r.waitFrame(r.submitted); err != nil {
			b.Fatal(err)
		}
	}
	for i := range 8 {
		frame(i)
	}
	gpuStart := gpuEngineTime(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		frame(i + 8)
	}
	b.StopTimer()
	b.ReportMetric(float64(gpuEngineTime(b)-gpuStart)/float64(b.N), "gpu-ns/op")
}

// BenchmarkComposeOpaqueTiles forces full redraws of four opaque covering tiles.
func BenchmarkComposeOpaqueTiles(b *testing.B) {
	const w, h = 2560, 1600
	r := newTestRenderer(b, w, h)
	contents := make(map[domain.WindowID]domain.SurfaceContent)
	scene := domain.Scene{Scale: 1, Background: "#101010"}
	for i := range 4 {
		c := solidContent(b, w/2, h/2, color.RGBA{R: byte(40 + i*50), A: 255})
		c.ID, c.Surface, c.Seq, c.Version, c.Opaque = domain.WindowID(i+1), uint64(i+1), 1, 1, true
		contents[c.ID] = c
		scene.Windows = append(scene.Windows, domain.SceneWindow{ID: c.ID, Rect: domain.Rect{X: i % 2 * (w / 2), Y: i / 2 * (h / 2), W: w / 2, H: h / 2}})
	}
	frame := func() {
		scene.Seq++
		if err := render(r, scene, contents); err != nil {
			b.Fatal(err)
		}
		if err := r.waitFrame(r.submitted); err != nil {
			b.Fatal(err)
		}
	}
	for range 8 {
		frame()
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		frame()
	}
}
