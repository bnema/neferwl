package vulkan

import (
	"context"
	"image"
	"os"
	"slices"
	"testing"
	"unsafe"

	"github.com/bnema/neferwl/internal/adapters/syncfile"
	"github.com/bnema/neferwl/internal/domain"

	"golang.org/x/sys/unix"
)

// udmabufMem is a udmabuf of size bytes and the memfd backing it, through
// which the test reads what the GPU wrote.
func udmabufMem(t *testing.T, size int) (buf *os.File, mem int) {
	t.Helper()
	mem, err := unix.MemfdCreate("capture-dst", unix.MFD_ALLOW_SEALING)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(mem) })
	if err := unix.Ftruncate(mem, int64(size)); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.FcntlInt(uintptr(mem), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK); err != nil {
		t.Fatal(err)
	}
	dev, err := os.OpenFile("/dev/udmabuf", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("udmabuf unavailable: %v", err)
	}
	defer func() { _ = dev.Close() }()
	arg := struct {
		memfd, flags uint32
		offset, size uint64
	}{memfd: uint32(mem), flags: 1, size: uint64(size)}
	fd, _, errno := unix.Syscall(unix.SYS_IOCTL, dev.Fd(), 0x40187542, uintptr(unsafe.Pointer(&arg)))
	if errno != 0 {
		t.Skipf("udmabuf create: %v", errno)
	}
	f := os.NewFile(fd, "capture-dst")
	t.Cleanup(func() { _ = f.Close() })
	return f, mem
}

// copyToWait captures region of the last frame into a fresh linear
// XRGB8888 dmabuf and returns its rows (w*4 bytes each).
func copyToWait(t *testing.T, r *Renderer, region image.Rectangle) []byte {
	t.Helper()
	return copyToWaitFormat(t, r, region, fourcc('X', 'R', '2', '4'))
}

// copyToWaitFormat is copyToWait into a linear dmabuf of fourcc.
func copyToWaitFormat(t *testing.T, r *Renderer, region image.Rectangle, fc uint32) []byte {
	t.Helper()
	xr24 := domain.DMABufFormat{Format: fc}
	if !slices.Contains(r.gpu.CaptureFormats(), xr24) {
		t.Skipf("linear %#x capture destination unavailable", fc)
	}
	w, h := region.Dx(), region.Dy()
	// Linear images want an aligned pitch: 256 bytes suits every driver.
	stride := (w*4 + 255) &^ 255
	size := (stride*h + 4095) &^ 4095
	f, mem := udmabufMem(t, size)
	dst := domain.DMABuf{ID: 1, Width: w, Height: h, Format: xr24.Format, Planes: []domain.DMABufPlane{{File: f, Stride: uint32(stride)}}}
	cf := captureWait(t, r)
	defer r.EndCapture(cf)
	done, err := cf.CopyTo(region, dst)
	if err != nil {
		t.Fatal(err)
	}
	if done != nil {
		defer func() { _ = done.Close() }()
		if err := syncfile.Wait(context.Background(), done); err != nil {
			t.Fatal(err)
		}
	}
	raw := make([]byte, size)
	if _, err := unix.Pread(mem, raw, 0); err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 0, w*h*4)
	for y := range h {
		out = append(out, raw[y*stride:y*stride+w*4]...)
	}
	return out
}

func TestCaptureCopyToDMABuf(t *testing.T) {
	r := newTestRenderer(t, 16, 8)
	requireCaptureSync(t, r)
	// A solid frame: every pixel of the region is the background, as BGRX.
	scene := domain.Scene{Background: "#0000ff"}
	if err := render(r, scene, nil); err != nil {
		t.Fatal(err)
	}
	px := copyToWait(t, r, image.Rect(4, 2, 12, 6))
	for i := 0; i < len(px); i += 4 {
		if px[i] != 0xff || px[i+1] != 0 || px[i+2] != 0 {
			t.Fatalf("pixel %d: %x", i/4, px[i:i+4])
		}
	}
	// An ARGB8888 destination gets opaque pixels, as Read gives SHM.
	px = copyToWaitFormat(t, r, image.Rect(4, 2, 12, 6), fourcc('A', 'R', '2', '4'))
	for i := 0; i < len(px); i += 4 {
		if px[i] != 0xff || px[i+1] != 0 || px[i+2] != 0 || px[i+3] != 0xff {
			t.Fatalf("ARGB pixel %d: %x", i/4, px[i:i+4])
		}
	}
	// The slot's copy is reaped once finished: the slots are reusable.
	for range captureSlots + 1 {
		_ = copyToWait(t, r, image.Rect(0, 0, 16, 8))
	}
}

// A translucent client over the background still gives opaque pixels in
// an ARGB8888 destination.
func TestCaptureCopyToARGBOpaque(t *testing.T) {
	r := newTestRenderer(t, 64, 16)
	requireCaptureSync(t, r)
	argb := domain.DMABufFormat{Format: fourcc('A', 'R', '2', '4')}
	if !slices.Contains(r.DMABuf().Formats, argb) {
		t.Skip("linear ARGB8888 not importable")
	}
	f := udmabuf(t, 64, 16, func(int, int) [4]byte { return [4]byte{0x40, 0x40, 0x40, 0x40} })
	buf := &domain.DMABuf{ID: 7, Width: 64, Height: 16, Format: argb.Format, Planes: []domain.DMABufPlane{{File: f, Stride: 256}}}
	scene := domain.Scene{Background: "#000000", Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 64, H: 16}}}}
	if err := render(r, scene, map[domain.WindowID]domain.SurfaceContent{1: {ID: 1, Width: 64, Height: 16, DMABuf: buf}}); err != nil {
		t.Fatal(err)
	}
	px := copyToWaitFormat(t, r, image.Rect(0, 0, 64, 16), argb.Format)
	for i := 0; i < len(px); i += 4 {
		if px[i+3] != 0xff || px[i] != 0x40 {
			t.Fatalf("pixel %d: %x", i/4, px[i:i+4])
		}
	}
}

func TestCaptureCopyToHDRFrame(t *testing.T) {
	r := hdrTestRenderer(t)
	requireCaptureSync(t, r)
	if err := render(r, domain.Scene{Seq: 1, Background: "#c08040"}, nil); err != nil {
		t.Fatal(err)
	}
	cf := captureWait(t, r)
	want := make([]byte, 4)
	if err := cf.Read(image.Rect(3, 3, 4, 4), want, 4); err != nil {
		t.Fatal(err)
	}
	r.EndCapture(cf)
	got := copyToWait(t, r, image.Rect(3, 3, 4, 4))
	if got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("dmabuf %x, readback %x", got, want)
	}
}

func TestCaptureCopyToRejects(t *testing.T) {
	r := newTestRenderer(t, 8, 8)
	requireCaptureSync(t, r)
	if err := render(r, domain.Scene{Background: "#123456"}, nil); err != nil {
		t.Fatal(err)
	}
	cf := captureWait(t, r)
	bad := []struct {
		region image.Rectangle
		dst    domain.DMABuf
	}{
		{image.Rect(0, 0, 9, 1), domain.DMABuf{Width: 9, Height: 1, Format: fourcc('X', 'R', '2', '4')}},
		{image.Rect(0, 0, 2, 2), domain.DMABuf{Width: 3, Height: 2, Format: fourcc('X', 'R', '2', '4')}},
		{image.Rect(0, 0, 2, 2), domain.DMABuf{Width: 2, Height: 2, Format: fourcc('X', 'R', '3', '0')}},
	}
	for _, b := range bad {
		if _, err := cf.CopyTo(b.region, b.dst); err == nil {
			t.Errorf("%v into %+v accepted", b.region, b.dst)
		}
	}
	r.EndCapture(cf)
	if _, err := cf.CopyTo(image.Rect(0, 0, 1, 1), domain.DMABuf{Width: 1, Height: 1, Format: fourcc('X', 'R', '2', '4')}); err == nil {
		t.Error("ended lease accepted")
	}
}
