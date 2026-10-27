package app

import (
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/drm/gbm"
	"github.com/bnema/neferwl/internal/adapters/vulkan"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// fourccXRGB8888 is DRM_FORMAT_XRGB8888.
const fourccXRGB8888 = 'X' | 'R'<<8 | '2'<<16 | '4'<<24

// renderNodeFor opens the render node of the GPU the renderer runs on.
func renderNodeFor(t *testing.T, dev uint64) *os.File {
	t.Helper()
	nodes, _ := filepath.Glob("/dev/dri/renderD*")
	for _, n := range nodes {
		var st unix.Stat_t
		if unix.Stat(n, &st) != nil || st.Rdev != dev {
			continue
		}
		f, err := os.OpenFile(n, os.O_RDWR|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Skipf("cannot open %s: %v", n, err)
		}
		return f
	}
	t.Skipf("no render node for device %#x", dev)
	return nil
}

// Images allocated by GBM are imported as the renderer's targets (the path
// the DRM output takes) and frames drawn into them read back. Skips without
// a GPU, libgbm or an openable render node.
func TestGBMImagesAreRenderTargets(t *testing.T) {
	const w, h = 128, 64
	inst, err := vulkan.NewInstance(zerowrap.Logger{})
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer inst.Close()
	gpu, err := inst.OpenGPU(domain.GPUInfo{})
	if err != nil {
		t.Skipf("Vulkan GPU unavailable: %v", err)
	}
	defer gpu.Close()
	renderer, err := gpu.NewRenderer(w, h, domain.RendererOptions{})
	r, _ := renderer.(*vulkan.Renderer)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	sup := r.DMABuf()
	if sup.Device == 0 || len(sup.Formats) == 0 {
		t.Skip("device does not import dmabufs")
	}
	mods := r.TargetModifiers(nil, false)
	if len(mods) == 0 {
		t.Skip("no importable target modifier")
	}
	t.Logf("renderer target modifiers: %#x", mods)
	node := renderNodeFor(t, sup.Device)
	defer node.Close()
	dev, err := gbm.Open(int(node.Fd()))
	if err != nil {
		t.Skipf("GBM unavailable: %v", err)
	}
	defer dev.Close()

	// A render node cannot always allocate scanout buffers.
	var bufs [2]*gbm.Buffer
	for _, usage := range []gbm.Usage{gbm.UseScanout | gbm.UseRendering, gbm.UseRendering} {
		var cerr error
		for i := range bufs {
			if bufs[i], cerr = dev.Create(w, h, fourccXRGB8888, mods, usage); cerr != nil {
				break
			}
		}
		if cerr == nil {
			t.Logf("GBM usage %#x", usage)
			break
		}
		t.Logf("GBM usage %#x: %v", usage, cerr)
		for i := range bufs {
			bufs[i].Close()
			bufs[i] = nil
		}
	}
	if bufs[0] == nil || bufs[1] == nil {
		t.Skip("GBM allocated no image with the renderer's modifiers")
	}
	defer bufs[0].Close()
	defer bufs[1].Close()

	var dmas []domain.DMABuf
	for _, b := range bufs {
		d, err := b.DMABuf()
		if err != nil {
			t.Fatal(err)
		}
		dmas = append(dmas, d)
	}
	defer func() {
		for _, d := range dmas {
			for _, p := range d.Planes {
				p.File.Close()
			}
		}
	}()
	t.Logf("GBM picked modifier %#x, %d plane(s), stride %d", dmas[0].Modifier, len(dmas[0].Planes), dmas[0].Planes[0].Stride)
	if err := r.ImportTargets(dmas); err != nil {
		t.Fatalf("ImportTargets: %v", err)
	}
	t.Log("Vulkan import of the GBM images succeeded")

	// Target 0: red background with a blue window; target 1: green.
	scenes := []domain.Scene{
		{Background: "#ff0000", Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{X: 32, Y: 16, W: 64, H: 32}}}},
		{Background: "#00ff00"},
	}
	contents := map[domain.WindowID]domain.SurfaceContent{1: {ID: 1, Width: 1, Height: 1, LogicalW: 64, LogicalH: 32, Opaque: true, Solid: &domain.SolidColor{B: 1, A: 1}}}
	type px struct {
		x, y int
		want color.RGBA
	}
	checks := [][]px{
		{{4, 4, color.RGBA{255, 0, 0, 255}}, {w - 4, h - 4, color.RGBA{255, 0, 0, 255}}, {64, 32, color.RGBA{0, 0, 255, 255}}, {36, 20, color.RGBA{0, 0, 255, 255}}},
		{{4, 4, color.RGBA{0, 255, 0, 255}}, {64, 32, color.RGBA{0, 255, 0, 255}}},
	}
	// Twice around, so each image is drawn into again after the other one.
	for round := range 2 {
		for i := range scenes {
			r.UseTarget(i)
			s := scenes[i]
			s.Seq = uint64(round*2 + i + 1)
			done, err := r.Render(s, contents)
			if err != nil {
				t.Fatalf("render %d: %v", i, err)
			}
			if done != nil {
				done.Close()
			}
			img := r.Pixels()
			if img == nil {
				t.Fatalf("target %d: no pixels", i)
			}
			for _, c := range checks[i] {
				if got := img.At(c.x, c.y); got != c.want {
					t.Errorf("round %d target %d pixel (%d,%d) = %v, want %v", round, i, c.x, c.y, got, c.want)
				}
			}
		}
	}
	if err := r.ImportTargets(nil); err != nil {
		t.Fatal(err)
	}
}
