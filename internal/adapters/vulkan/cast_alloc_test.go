package vulkan

import (
	"context"
	"encoding/binary"
	"image"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/syncfile"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"

	"golang.org/x/sys/unix"
)

// castLinear allocates a linear w×h cast buffer of fc, skipping when the
// device has none.
func castLinear(t *testing.T, g *GPU, fc uint32, w, h int) domain.DMABuf {
	t.Helper()
	if !slices.Contains(g.CastFormats(), domain.DMABufFormat{Format: fc}) {
		t.Skipf("linear cast format %#x unavailable", fc)
	}
	buf, err := g.AllocCast(fc, w, h, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		g.ReleaseCast(buf)
		for _, p := range buf.Planes {
			_ = p.File.Close()
		}
	})
	if len(buf.Planes) != 1 || buf.Planes[0].Stride < uint32(w*4) || buf.Width != w || buf.Height != h || buf.Format != fc {
		t.Fatalf("cast buffer %+v", buf)
	}
	return buf
}

// castSubmit copies region of cf into buf and waits for the copy.
func castSubmit(t *testing.T, cf ports.CaptureFrame, region image.Rectangle, buf domain.DMABuf) {
	t.Helper()
	done, err := cf.CopyTo(region, buf)
	if err != nil {
		t.Fatal(err)
	}
	if done != nil {
		defer done.Close()
		if err := syncfile.Wait(context.Background(), done); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCastFormatsListsExportableModifiers(t *testing.T) {
	g := newTestGPU(t)
	formats := g.CastFormats()
	if len(formats) == 0 {
		t.Skip("no cast formats")
	}
	for _, f := range formats {
		if !slices.Contains(castFourccs[:], f.Format) || retilesDCC(f.Modifier) {
			t.Errorf("unexpected cast format %+v", f)
		}
	}
	// Every listed modifier exports.
	for _, f := range formats {
		buf, err := g.AllocCast(f.Format, 64, 32, f.Modifier)
		if err != nil {
			t.Errorf("AllocCast %+v: %v", f, err)
			continue
		}
		if buf.Modifier != f.Modifier || len(buf.Planes) < 1 {
			t.Errorf("AllocCast %+v gave %+v", f, buf)
		}
		for _, p := range buf.Planes {
			_ = p.File.Close()
		}
	}
	if _, err := g.AllocCast(fourcc('N', 'V', '1', '2'), 64, 32, 0); err == nil {
		t.Error("NV12 cast buffer allocated")
	}
	if _, err := g.AllocCast(fourcc('X', 'R', '2', '4'), 0, 32, 0); err == nil {
		t.Error("empty cast buffer allocated")
	}
}

func TestCastCopySDR(t *testing.T) {
	r := newTestRenderer(t, 16, 8)
	requireCaptureSync(t, r)
	if err := render(r, domain.Scene{Background: "#0000ff"}, nil); err != nil {
		t.Fatal(err)
	}
	buf := castLinear(t, r.gpu, fourcc('X', 'R', '2', '4'), 16, 8)
	cf := captureWait(t, r)
	defer r.EndCapture(cf)
	castSubmit(t, cf, image.Rect(0, 0, 16, 8), buf)
	// The same buffer again: the copy waits for the buffer's fences.
	castSubmit(t, cf, image.Rect(0, 0, 16, 8), buf)
	r.EndCapture(cf)
	// The consumer's view: the buffer imported as a client surface.
	buf.ID = 77
	scene := domain.Scene{Background: "#000000", Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 16, H: 8}}}}
	if err := render(r, scene, map[domain.WindowID]domain.SurfaceContent{1: {ID: 1, Width: 16, Height: 8, Opaque: true, DMABuf: &buf}}); err != nil {
		t.Skipf("cast buffer not importable as a surface: %v", err)
	}
	img := r.Pixels()
	if img == nil {
		t.Fatal("no pixels")
	}
	for y := range 8 {
		for x := range 16 {
			if c := img.RGBAAt(x, y); c.B != 0xff || c.R != 0 || c.G != 0 {
				t.Fatalf("pixel %d,%d: %v", x, y, c)
			}
		}
	}
	cf = captureWait(t, r)
	// An 8-bit capture is not copied into a PQ buffer.
	pq := castLinear(t, r.gpu, fourcc('X', 'B', '3', '0'), 16, 8)
	if _, err := cf.CopyTo(image.Rect(0, 0, 16, 8), pq); err == nil {
		t.Error("SDR capture copied into XB30")
	}
}

func TestCastCopyPQ(t *testing.T) {
	r := hdrTestRenderer(t)
	requireCaptureSync(t, r)
	scene, contents, _ := hdrPQScene(t, r)
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	display := pqTargetWords(t, r)
	_ = castLinear(t, r.gpu, fourcc('X', 'B', '3', '0'), r.width, r.height)
	// Device-local cast memory is not mappable: the copy is checked in a
	// linear udmabuf of the same layout.
	stride := (r.width*4 + 255) &^ 255
	size := (stride*r.height + 4095) &^ 4095
	file, mem := udmabufMem(t, size)
	buf := domain.DMABuf{ID: 1, Width: r.width, Height: r.height, Format: fourcc('X', 'B', '3', '0'), Planes: []domain.DMABufPlane{{File: file, Stride: uint32(stride)}}}
	f, err := r.BeginCapturePQ()
	if err != nil {
		t.Fatal(err)
	}
	defer r.EndCapture(f)
	if done := f.Done(); done != nil {
		if err := syncfile.Wait(context.Background(), done); err != nil {
			t.Fatal(err)
		}
	}
	castSubmit(t, f, image.Rect(0, 0, r.width, r.height), buf)
	raw := make([]byte, size)
	if _, err := unix.Pread(mem, raw, 0); err != nil {
		t.Fatal(err)
	}
	for i, w := range display {
		x, y := i%r.width, i/r.width
		got := binary.LittleEndian.Uint32(raw[y*stride+x*4:]) &^ (3 << 30)
		if want := SwizzleXR30ToXB30(w); got != want {
			t.Fatalf("pixel %d: got %#08x want %#08x", i, got, want)
		}
	}
	xr := castLinear(t, r.gpu, fourcc('X', 'R', '2', '4'), r.width, r.height)
	if _, err := f.CopyTo(image.Rect(0, 0, r.width, r.height), xr); err == nil {
		t.Error("PQ capture copied into XR24")
	}
}
