package vulkan

import (
	"context"
	"image/color"
	"slices"
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/adapters/syncfile"
	"github.com/bnema/nefertty/internal/ports"
)

// near reports whether two colors are within tol per channel.
func near(a, b color.RGBA, tol int) bool {
	d := func(x, y uint8) bool { v := int(x) - int(y); return v >= -tol && v <= tol }
	return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B)
}

// Scaled wl_shm pixels filter linearly: halfway between a black and a
// white texel is grey, not either one (nearest neighbour).
func TestRendererScaledSHMFiltersLinearly(t *testing.T) {
	r, err := New(64, 16)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	// 2×1 buffer: black, white; drawn 64×16, texel centres at x=16, x=48.
	px := []byte{0, 0, 0, 255, 255, 255, 255, 255}
	c := shmContent(t, 2, 1, 8, px)
	c.LogicalW, c.LogicalH = 64, 16
	scene := ports.Scene{Background: "#ff0000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 16}, Borderless: true}}}
	if _, err := r.Render(scene, map[ports.WindowID]ports.SurfaceContent{1: *c}); err != nil {
		t.Fatal(err)
	}
	got := r.Pixels().RGBAAt(32, 8)
	if !near(got, color.RGBA{128, 128, 128, 255}, 8) {
		t.Fatalf("midpoint %v, want grey", got)
	}
	if got := r.Pixels().RGBAAt(2, 8); !near(got, color.RGBA{0, 0, 0, 255}, 2) {
		t.Fatalf("left edge %v, want black (clamped)", got)
	}
}

// Scaled dmabufs are sampled with the linear sampler.
func TestRendererScaledDMABufFiltersLinearly(t *testing.T) {
	r, err := New(128, 4)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	linear := ports.DMABufFormat{Format: fourcc('A', 'R', '2', '4'), Modifier: 0}
	if !slices.Contains(r.DMABuf().Formats, linear) {
		t.Skipf("linear ARGB8888 not importable")
	}
	// 64×1: left half black, right half white, drawn twice as wide.
	f := udmabuf(t, 64, 1, func(x, _ int) [4]byte {
		if x < 32 {
			return [4]byte{0, 0, 0, 255}
		}
		return [4]byte{255, 255, 255, 255}
	})
	buf := &ports.DMABuf{ID: 3, Width: 64, Height: 1, Format: linear.Format, Planes: []ports.DMABufPlane{{File: f, Stride: 256}}}
	scene := ports.Scene{Background: "#ff0000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 128, H: 4}, Borderless: true}}}
	c := ports.SurfaceContent{ID: 1, Width: 64, Height: 1, LogicalW: 128, LogicalH: 4, DMABuf: buf}
	if _, err := r.Render(scene, map[ports.WindowID]ports.SurfaceContent{1: c}); err != nil {
		t.Fatal(err)
	}
	// Target x=64 (centre 64.5) maps to buffer 32.25: between texel 31
	// (black, centre 31.5) and 32 (white, centre 32.5), 75% white.
	if got := r.Pixels().RGBAAt(64, 2); !near(got, color.RGBA{191, 191, 191, 255}, 10) {
		t.Fatalf("boundary %v, want a linear mix", got)
	}
}

// Render returns a sync file that becomes readable when the GPU is done.
func TestRendererReturnsDoneFence(t *testing.T) {
	r, err := New(32, 32)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	if !r.syncFD {
		t.Skip("device cannot export sync files")
	}
	done, err := r.Render(ports.Scene{Background: "#102030"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done == nil {
		t.Fatal("no done fence")
	}
	defer done.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := syncfile.Wait(ctx, done); err != nil {
		t.Fatal(err)
	}
}

// An unchanged wl_shm window is not copied again; a new content is.
func TestRendererSHMCopiedOncePerContent(t *testing.T) {
	r, err := New(32, 32)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	c := solidContent(t, 16, 16, color.RGBA{1, 2, 3, 255})
	c.ID, c.Seq = 1, 1
	scene := ports.Scene{Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 16, H: 16}, Borderless: true}}}
	contents := map[ports.WindowID]ports.SurfaceContent{1: c}
	render := func() {
		t.Helper()
		done, err := r.Render(scene, contents)
		if done != nil {
			done.Close()
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	render()
	if r.copied != 16*16*4 {
		t.Fatalf("first frame copied %d", r.copied)
	}
	for range 3 {
		render()
	}
	if r.copied != 16*16*4 {
		t.Fatalf("unchanged content copied again: %d", r.copied)
	}
	c.Seq = 2
	contents[1] = c
	render()
	if r.copied != 2*16*16*4 {
		t.Fatalf("new content copied %d", r.copied)
	}
}
