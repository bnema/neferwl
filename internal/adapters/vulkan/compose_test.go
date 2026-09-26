package vulkan

import (
	"context"
	"image/color"
	"os"
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
	scene := ports.Scene{Background: "#ff0000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 16}}}}
	if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: *c}); err != nil {
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
	scene := ports.Scene{Background: "#ff0000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 128, H: 4}}}}
	c := ports.SurfaceContent{ID: 1, Width: 64, Height: 1, LogicalW: 128, LogicalH: 4, DMABuf: buf}
	if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: c}); err != nil {
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
	scene := ports.Scene{Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 16, H: 16}}}}
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

// A surface fully hidden by an opaque subsurface above it is not copied:
// only the subsurface's bytes are, and the frame shows it.
func TestRendererSkipsOccludedSurface(t *testing.T) {
	r, err := New(32, 32)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	green := color.RGBA{0, 255, 0, 255}
	root := solidContent(t, 16, 16, color.RGBA{255, 0, 0, 255})
	root.ID, root.Seq = 1, 1
	game := solidContent(t, 16, 16, green)
	game.Opaque = true
	root.Children = []ports.Subsurface{{SurfaceContent: game}}
	scene := ports.Scene{Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 16, H: 16}}}}
	if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: root}); err != nil {
		t.Fatal(err)
	}
	if r.copied != 16*16*4 {
		t.Fatalf("copied %d bytes, want only the subsurface's %d", r.copied, 16*16*4)
	}
	if got := r.Pixels().At(8, 8); got != green {
		t.Fatalf("At(8,8)=%v want %v", got, green)
	}
	// A smaller opaque child leaves part of the root visible: it is copied.
	small := solidContent(t, 8, 8, green)
	small.Opaque = true
	root.Seq = 2
	root.Children = []ports.Subsurface{{SurfaceContent: small}}
	r.copied = 0
	if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: root}); err != nil {
		t.Fatal(err)
	}
	if r.copied != 16*16*4+8*8*4 {
		t.Fatalf("partly hidden root: copied %d", r.copied)
	}
}

// render draws a frame and closes its fence.
func render(r *Renderer, s ports.Scene, contents map[ports.WindowID]ports.SurfaceContent) error {
	done, err := r.Render(s, contents)
	if done != nil {
		done.Close()
	}
	return err
}

// A wl_shm window's GPU buffer is rewritten only after every frame that
// read it completed: contents 1, 2, 3 back to back alternate two buffers,
// and the third copy waits for the frame that drew the first (the slot
// ring and shmCopyFor's waitFrame both guarantee it).
func TestRendererSHMDoubleBufferWaitsForReaders(t *testing.T) {
	r, err := New(32, 32)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	if !r.syncFD {
		t.Skip("frames are finished on return without sync files")
	}
	scene := ports.Scene{Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 16, H: 16}}}}
	var readers []uint64
	for seq := uint64(1); seq <= 3; seq++ {
		c := solidContent(t, 16, 16, color.RGBA{uint8(seq * 60), 0, 0, 255})
		c.ID, c.Seq = 1, seq
		if seq == 3 {
			// The buffer about to be reused was last read by frame 1.
			if r.completed >= readers[0] {
				t.Fatalf("frame %d already known done before the reuse", readers[0])
			}
		}
		if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: c}); err != nil {
			t.Fatal(err)
		}
		readers = append(readers, r.frame)
		if seq == 3 && r.completed < readers[0] {
			t.Fatalf("buffer of frame %d rewritten with completed=%d", readers[0], r.completed)
		}
	}
	if got := r.Pixels().RGBAAt(4, 4); got != (color.RGBA{180, 0, 0, 255}) {
		t.Fatalf("pixel %v", got)
	}
}

// A content's explicit acquire fence becomes a wait semaphore; a file
// that is not a sync file is refused (no semaphore) without failing the
// frame, and the caller's file stays open.
func TestRendererImportsAcquireFence(t *testing.T) {
	r, err := New(8, 8)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	done, err := r.Render(ports.Scene{Background: "#000000"}, nil)
	if err != nil || done == nil {
		t.Skipf("no sync file: %v", err)
	}
	defer done.Close()
	sem := r.importFence(done)
	if sem == 0 {
		t.Fatal("sync file not imported")
	}
	r.dd.DestroySemaphore(r.device, sem, nil)
	if _, err := done.Stat(); err != nil {
		t.Fatal("caller's fence closed")
	}
	f, w, _ := os.Pipe()
	defer f.Close()
	defer w.Close()
	if sem := r.importFence(f); sem != 0 {
		t.Fatal("pipe imported as a fence")
	}
}
