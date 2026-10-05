package vulkan

import (
	"image"
	"image/color"
	"os"
	"slices"
	"testing"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

// udmabuf turns memfd pages into a real dmabuf: a linear buffer any GPU
// driver imports, like a client's.
func udmabuf(t testing.TB, w, h int, fill func(x, y int) [4]byte) *os.File {
	t.Helper()
	dev, err := os.OpenFile("/dev/udmabuf", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("udmabuf unavailable: %v", err)
	}
	defer dev.Close()
	size := (w*h*4 + os.Getpagesize() - 1) / os.Getpagesize() * os.Getpagesize()
	mem, err := unix.MemfdCreate("dmabuf-test", unix.MFD_ALLOW_SEALING)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(mem)
	if err := unix.Ftruncate(mem, int64(size)); err != nil {
		t.Fatal(err)
	}
	data, err := unix.Mmap(mem, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	for y := range h {
		for x := range w {
			p := fill(x, y)
			copy(data[(y*w+x)*4:], p[:])
		}
	}
	_ = unix.Munmap(data)
	if _, err := unix.FcntlInt(uintptr(mem), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK); err != nil {
		t.Fatal(err)
	}
	// struct udmabuf_create { u32 memfd; u32 flags; u64 offset; u64 size; }
	arg := struct {
		memfd, flags uint32
		offset, size uint64
	}{memfd: uint32(mem), flags: 1 /* CLOEXEC */, size: uint64(size)}
	const udmabufCreate = 0x40187542 // _IOW('u', 0x42, struct udmabuf_create)
	fd, _, errno := unix.Syscall(unix.SYS_IOCTL, dev.Fd(), udmabufCreate, uintptr(unsafe.Pointer(&arg)))
	if errno != 0 {
		t.Skipf("udmabuf create: %v", errno)
	}
	f := os.NewFile(fd, "dmabuf")
	t.Cleanup(func() { f.Close() })
	return f
}

// A linear ARGB8888 dmabuf is imported and drawn like wl_shm pixels, and
// the import is freed once no frame draws it.
func TestRendererImportsDMABuf(t *testing.T) {
	r, err := New(80, 32)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	linear := ports.DMABufFormat{Format: fourcc('A', 'R', '2', '4'), Modifier: 0}
	if !slices.Contains(r.DMABuf().Formats, linear) {
		t.Skipf("linear ARGB8888 not importable: %+v", r.DMABuf())
	}
	// Left half red, right half blue (B,G,R,A in memory). 64 pixels wide:
	// drivers want linear rows aligned (256 bytes on AMD).
	f := udmabuf(t, 64, 16, func(x, _ int) [4]byte {
		if x < 32 {
			return [4]byte{0, 0, 255, 255}
		}
		return [4]byte{255, 0, 0, 255}
	})
	buf := &ports.DMABuf{ID: 7, Width: 64, Height: 16, Format: linear.Format, Planes: []ports.DMABufPlane{{File: f, Stride: 256}}}
	scene := ports.Scene{Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{X: 4, Y: 4, W: 64, H: 16}}}}
	contents := map[ports.WindowID]ports.SurfaceContent{1: {ID: 1, Width: 64, Height: 16, DMABuf: buf}}
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	px := readPixels(t, r)
	for _, c := range []struct {
		x, y int
		want color.RGBA
	}{{4, 4, color.RGBA{255, 0, 0, 255}}, {35, 19, color.RGBA{255, 0, 0, 255}}, {36, 4, color.RGBA{0, 0, 255, 255}}, {67, 19, color.RGBA{0, 0, 255, 255}}, {2, 2, color.RGBA{0, 0, 0, 255}}, {70, 10, color.RGBA{0, 0, 0, 255}}} {
		if got := px.At(c.x, c.y); got != c.want {
			t.Errorf("At(%d,%d)=%v want %v", c.x, c.y, got, c.want)
		}
	}
	if len(r.imports) != 1 {
		t.Fatalf("imports %d", len(r.imports))
	}
	// Frames without the buffer: its import is released after importTTL.
	for range importTTL + 1 {
		if err := render(r, scene, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.imports) != 0 {
		t.Fatalf("import kept: %d", len(r.imports))
	}
}

// The HDR swapchain's DRM 2101010 formats use packed UNORM views, not
// automatic sRGB decoding. The same imports work in SDR composition.
func TestTenBitDMABufFormats(t *testing.T) {
	for _, tc := range []struct {
		name   [4]byte
		opaque bool
	}{
		{[4]byte{'A', 'R', '3', '0'}, false},
		{[4]byte{'X', 'R', '3', '0'}, true},
		{[4]byte{'A', 'B', '3', '0'}, false},
		{[4]byte{'X', 'B', '3', '0'}, true},
	} {
		format, opaque, ok := vkFormat(fourcc(tc.name[0], tc.name[1], tc.name[2], tc.name[3]))
		if !ok || format == 0 || opaque != tc.opaque {
			t.Errorf("%q: format %v opaque %t available %t", tc.name, format, opaque, ok)
		}
	}
}

// A subsurface dmabuf is read after its own acquire fence, not the root's.
func TestRendererSubsurfaceAcquire(t *testing.T) {
	r, err := New(80, 32)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	linear := ports.DMABufFormat{Format: fourcc('A', 'R', '2', '4'), Modifier: 0}
	if !slices.Contains(r.DMABuf().Formats, linear) {
		t.Skipf("linear ARGB8888 not importable: %+v", r.DMABuf())
	}
	f := udmabuf(t, 64, 16, func(int, int) [4]byte { return [4]byte{0, 0, 255, 255} })
	fence, w, _ := os.Pipe()
	defer fence.Close()
	defer w.Close()
	child := ports.SurfaceContent{ID: 1, Width: 64, Height: 16, Acquire: fence,
		DMABuf: &ports.DMABuf{ID: 8, Width: 64, Height: 16, Format: linear.Format, Planes: []ports.DMABufPlane{{File: f, Stride: 256}}}}
	scene := ports.Scene{Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 16}}}}
	contents := map[ports.WindowID]ports.SurfaceContent{1: {ID: 1, Width: 64, Height: 16, Children: []ports.Subsurface{{SurfaceContent: child}}}}
	ds := r.draws(scene, contents, newDamage(r.target(), scene, image.Rect(0, 0, 80, 32)))
	for _, d := range ds {
		if d.im != nil && d.acquire == fence {
			return
		}
	}
	t.Fatal("subsurface draw has no acquire fence")
}
