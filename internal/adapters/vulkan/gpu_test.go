package vulkan

import (
	"errors"
	"image/color"
	"slices"
	"sync"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"

	vk "github.com/bnema/purego-vulkan/vulkan"
	"golang.org/x/sys/unix"
)

func TestMatchDRM(t *testing.T) {
	p := vk.PhysicalDeviceDrmPropertiesEXT{HasPrimary: 1, PrimaryMajor: 226, PrimaryMinor: 1, HasRender: 1, RenderMajor: 226, RenderMinor: 128}
	for _, tt := range []struct {
		name            string
		p               vk.PhysicalDeviceDrmPropertiesEXT
		primary, render uint64
		want            bool
	}{
		{"render", p, 0, unix.Mkdev(226, 128), true},
		{"primary", p, unix.Mkdev(226, 1), 0, true},
		{"none", p, unix.Mkdev(226, 2), unix.Mkdev(226, 129), false},
		{"absent render", vk.PhysicalDeviceDrmPropertiesEXT{RenderMajor: 226, RenderMinor: 128}, 0, unix.Mkdev(226, 128), false},
		{"absent primary", vk.PhysicalDeviceDrmPropertiesEXT{PrimaryMajor: 226, PrimaryMinor: 1}, unix.Mkdev(226, 1), 0, false},
		{"zero", vk.PhysicalDeviceDrmPropertiesEXT{HasRender: 1, HasPrimary: 1}, 0, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchDRM(tt.p, tt.primary, tt.render); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQueuePool(t *testing.T) {
	for _, n := range []int{1, 2, 8} {
		t.Run(string(rune('0'+n)), func(t *testing.T) {
			p := make(queuePool, n)
			for i := range p {
				p[i] = &queue{}
			}
			for i := range p {
				if got := p.acquire(); got != p[i] {
					t.Fatalf("queue %d not dedicated", i)
				}
			}
			if got := p.acquire(); got != p[0] || got.users != 2 {
				t.Fatal("exhaustion did not share queue zero")
			}
			p.release(p[0])
			if p[0].users != 1 {
				t.Fatal("shared release freed busy queue")
			}
			last := p[n-1]
			p.release(last)
			if got := p.acquire(); got != last {
				t.Fatal("released dedicated queue not reused")
			}
			for _, q := range p {
				p.release(q)
				if q.users != 0 {
					t.Fatal("queue still in use")
				}
			}
		})
	}
	var empty queuePool
	if empty.acquire() != nil {
		t.Fatal("empty pool returned queue")
	}
}

func TestChecked(t *testing.T) {
	if checked("success", vk.Success) != nil {
		t.Fatal("success returned error")
	}
	err := checked("lost", vk.ErrorDeviceLost)
	if !errors.Is(err, ports.ErrDeviceLost) {
		t.Fatalf("missing sentinel: %v", err)
	}
	if errors.Is(checked("other", vk.ErrorOutOfDeviceMemory), ports.ErrDeviceLost) {
		t.Fatal("ordinary error marked lost")
	}
}

func TestTwoRenderersShareGPU(t *testing.T) {
	g := newTestGPU(t)
	var rs [2]*Renderer
	for i := range rs {
		renderer, err := g.NewRenderer(32, 32, ports.RendererOptions{})
		if err != nil {
			t.Fatal(err)
		}
		rs[i] = renderer.(*Renderer)
		t.Cleanup(rs[i].Close)
	}
	if rs[0].gpu != g || rs[1].gpu != g || rs[0].gpu.device == 0 {
		t.Fatal("renderers do not share one GPU device")
	}
	c := solidContent(t, 16, 16, color.RGBA{1, 2, 3, 255})
	c.ID, c.Seq, c.Version = 1, 1, 1
	scene := domain.Scene{Background: "#000000", Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 16, H: 16}}}}
	contents := map[domain.WindowID]domain.SurfaceContent{1: c}
	for frame := range 10 {
		scene.Seq = uint64(frame + 1)
		for i, r := range rs {
			if err := render(r, scene, contents); err != nil {
				t.Fatalf("renderer %d frame %d: %v", i, frame, err)
			}
		}
	}
	for i, r := range rs {
		pixels := r.Pixels()
		if pixels == nil || pixels.RGBAAt(8, 8) != (color.RGBA{1, 2, 3, 255}) {
			t.Fatalf("renderer %d has incorrect pixels", i)
		}
	}
	rs[0].SetHDR(203)
	rs[0].Close()
	for frame := range 5 {
		scene.Seq = uint64(11 + frame)
		if err := render(rs[1], scene, contents); err != nil {
			t.Fatalf("surviving renderer frame %d: %v", frame, err)
		}
	}
	pixels := rs[1].Pixels()
	if pixels == nil || pixels.RGBAAt(8, 8) != (color.RGBA{1, 2, 3, 255}) {
		t.Fatal("surviving renderer has incorrect pixels")
	}

}

func TestConcurrentRendererTargets(t *testing.T) {
	g := newTestGPU(t)
	if g.dmabuf.Device == 0 {
		t.Skip("no exportable GPU")
	}
	baseline := slices.Clone(g.hdrMods)
	var rs [2]*Renderer
	for i := range rs {
		renderer, err := g.NewRenderer(32, 32, ports.RendererOptions{Virtual: true, Readback: i == 0})
		if err != nil {
			t.Fatal(err)
		}
		rs[i] = renderer.(*Renderer)
		t.Cleanup(rs[i].Close)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, r := range rs {
		wg.Go(func() {
			r.SetHDR(203)
			// Exercise concurrent SetHDR, ExportTargets and Render sharing one GPU queue.
			_ = r.TargetModifiers(nil, true)
			bufs, err := r.ExportTargets(2, nil, true)
			if err != nil {
				errs <- err
				return
			}
			defer func() {
				r.ExportTargets(0, nil, false)
				for _, b := range bufs {
					for _, p := range b.Planes {
						p.File.Close()
					}
				}
			}()
			for frame := range 3 {
				if err := render(r, domain.Scene{Seq: uint64(frame + 1), Background: "#102030"}, nil); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if !slices.Equal(rs[1].exportModifiers(nil, false), baseline) {
		t.Fatal("readback output narrowed non-readback HDR modifiers")
	}
}

func TestClosedGPUConstructors(t *testing.T) {
	g := &GPU{syncFD: true}
	for _, tc := range []struct {
		name   string
		create func() error
	}{
		{"renderer", func() error { _, err := g.NewRenderer(16, 16, ports.RendererOptions{}); return err }},
		{"copier", func() error { _, err := g.NewCopier(16, 16); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.create(); err == nil || err.Error() != "vulkan: GPU closed" {
				t.Fatalf("got %v, want GPU closed", err)
			}
		})
	}
}
