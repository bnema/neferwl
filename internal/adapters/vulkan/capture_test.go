package vulkan

import (
	"context"
	"encoding/binary"
	"errors"
	"image"
	"math"
	"os"
	"slices"
	"testing"
	"unsafe"

	"image/color"

	"github.com/bnema/neferwl/internal/adapters/syncfile"
	"github.com/bnema/neferwl/internal/ports"
	vk "github.com/bnema/purego-vulkan/vulkan"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// captureWait leases a capture of the last frame and waits for it.
func captureWait(t *testing.T, r *Renderer) ports.CaptureFrame {
	t.Helper()
	cf, err := r.BeginCapture()
	if err != nil {
		t.Fatal(err)
	}
	if done := cf.Done(); done != nil {
		if err := syncfile.Wait(context.Background(), done); err != nil {
			t.Fatal(err)
		}
	}
	return cf
}

func TestCaptureLastFrameRegion(t *testing.T) {
	r, err := New(8, 8)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	if _, err := r.BeginCapture(); err == nil {
		t.Fatal("capture before any frame accepted")
	}
	if err := render(r, ports.Scene{Background: "#123456"}, nil); err != nil {
		t.Fatal(err)
	}
	if r.captures != nil {
		t.Fatal("slots allocated before the first capture")
	}
	cf := captureWait(t, r)
	if len(r.captures) != captureSlots {
		t.Fatalf("%d slots", len(r.captures))
	}
	dst := make([]byte, 24)
	if err := cf.Read(image.Rect(2, 3, 4, 5), dst, 12); err != nil {
		t.Fatal(err)
	}
	for _, at := range []int{0, 4, 12, 16} {
		if got := dst[at : at+4]; got[0] != 0x56 || got[1] != 0x34 || got[2] != 0x12 || got[3] != 255 {
			t.Fatalf("pixel %d: %x", at, got)
		}
	}
	if err := cf.Read(image.Rect(-1, 0, 1, 1), dst, 12); err == nil {
		t.Fatal("invalid region accepted")
	}
	if err := cf.Read(image.Rect(0, 0, 2, 2), dst[:10], 8); err == nil {
		t.Fatal("short destination accepted")
	}
	// A stride near MaxInt must not overflow the row arithmetic.
	if err := cf.Read(image.Rect(0, 0, 2, 2), dst, math.MaxInt-1); err == nil {
		t.Fatal("overflowing stride accepted")
	}
	if err := cf.Read(image.Rect(0, 0, 2, 2), dst, math.MaxInt); err == nil {
		t.Fatal("MaxInt stride accepted")
	}
	// A one-row read needs only w*4 bytes whatever the stride.
	if err := cf.Read(image.Rect(0, 0, 2, 1), dst[:8], math.MaxInt); err != nil {
		t.Fatal(err)
	}
	r.EndCapture(cf)
	if err := cf.Read(image.Rect(2, 3, 4, 5), dst, 12); err == nil {
		t.Fatal("read after EndCapture accepted")
	}
	r.EndCapture(cf) // idempotent
}

// Both slots leased: the third capture fails at once; returning one
// frees it again once its copy finished.
func TestCaptureSlotsBounded(t *testing.T) {
	r, err := New(8, 8)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	if err := render(r, ports.Scene{Background: "#ff0000"}, nil); err != nil {
		t.Fatal(err)
	}
	a := captureWait(t, r)
	b := captureWait(t, r)
	if _, err := r.BeginCapture(); !errors.Is(err, ports.ErrCaptureBusy) {
		t.Fatalf("third capture: %v", err)
	}
	r.EndCapture(a)
	// a's copy finished (waited above): its fence polls signalled.
	c := captureWait(t, r)
	dst := make([]byte, 4)
	if err := c.Read(image.Rect(0, 0, 1, 1), dst, 4); err != nil {
		t.Fatal(err)
	}
	if dst[2] != 255 || dst[1] != 0 || dst[0] != 0 {
		t.Fatalf("pixel %v", dst)
	}
	r.EndCapture(b)
	r.EndCapture(c)
	// Pixels never takes a leased slot: with both out it reports nil.
	a, b = captureWait(t, r), captureWait(t, r)
	if img := r.Pixels(); img != nil {
		t.Fatal("Pixels stole a leased slot")
	}
	r.EndCapture(a)
	r.EndCapture(b)
	if img := r.Pixels(); img == nil || img.RGBAAt(0, 0) != (color.RGBA{255, 0, 0, 255}) {
		t.Fatalf("Pixels after release: %v", img)
	}
	// Frames of a closed renderer are invalid, and Close survives leases
	// still out.
	d := captureWait(t, r)
	r.Close()
	if err := d.Read(image.Rect(0, 0, 1, 1), dst, 4); err == nil {
		t.Fatal("read after Close accepted")
	}
	r.EndCapture(d)
}

// A returned slot whose copy has not finished is not reused: the poll
// sees the fence unsignalled. Simulated by marking the slot pending with
// its fence reset.
func TestCaptureReturnedSlotWaitsForFence(t *testing.T) {
	r, err := New(8, 8)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	if err := render(r, ports.Scene{Background: "#00ff00"}, nil); err != nil {
		t.Fatal(err)
	}
	a := captureWait(t, r)
	b := captureWait(t, r)
	r.EndCapture(a)
	r.EndCapture(b)
	for _, s := range r.captures {
		// The copies finished (waited above); reset only signalled fences.
		if err := checked("vkWaitForFences", r.dd.WaitForFences(r.device, 1, &s.fence, 1, math.MaxUint64)); err != nil {
			t.Fatal(err)
		}
		if err := checked("vkResetFences", r.dd.ResetFences(r.device, 1, &s.fence)); err != nil {
			t.Fatal(err)
		}
		s.pending = true
	}
	if _, err := r.BeginCapture(); !errors.Is(err, ports.ErrCaptureBusy) {
		t.Fatalf("pending slots reused: %v", err)
	}
	// Signal them again with an empty submit, then the poll frees them.
	for _, s := range r.captures {
		if err := checked("vkQueueSubmit", r.dd.QueueSubmit(r.queue, 0, nil, s.fence)); err != nil {
			t.Fatal(err)
		}
		if err := checked("vkWaitForFences", r.dd.WaitForFences(r.device, 1, &s.fence, 1, math.MaxUint64)); err != nil {
			t.Fatal(err)
		}
	}
	c := captureWait(t, r)
	r.EndCapture(c)
	if _, err := r.captureSize(); err != nil {
		t.Fatal(err)
	}
	if r.captures[0].fence == 0 || r.captures[0].done == vk.Semaphore(0) {
		t.Fatal("slot objects missing")
	}
}

// The capture size cap admits two 5K slots and rejects 8K.
func TestCaptureSizeCap(t *testing.T) {
	r := &Renderer{width: 5120, height: 2880}
	if _, err := r.captureSize(); err != nil {
		t.Fatalf("5K rejected: %v", err)
	}
	r.width, r.height = 7680, 4320
	if _, err := r.captureSize(); err == nil {
		t.Fatal("8K accepted")
	}
	r.width, r.height = 0, 4320
	if _, err := r.captureSize(); err == nil {
		t.Fatal("empty accepted")
	}
}

// captureSDR follows the documented curve: exact below the knee, a
// monotonic shoulder above it that never saturates, hue kept, and
// negative components desaturated toward luminance.
func TestCaptureCurve(t *testing.T) {
	for _, x := range []float64{0, 0.25, 0.5, 0.9} {
		if got := captureCurve(x); got != x {
			t.Fatalf("curve(%v) = %v", x, got)
		}
	}
	prev := captureCurve(0.9)
	for _, x := range []float64{1, 1.5, 2, 4.93, 10, 100} {
		got := captureCurve(x)
		if got <= prev || got >= 1 {
			t.Fatalf("curve(%v) = %v after %v", x, got, prev)
		}
		prev = got
	}
	// Diffuse white stays bright; 2x and 5x (1000 nits) white stay
	// distinct from each other and from saturation.
	white := captureSDR([3]float64{1, 1, 1})
	twice := captureSDR([3]float64{2, 2, 2})
	peak := captureSDR([3]float64{4.93, 4.93, 4.93})
	if white[0] < 240 || white[0] != white[1] || white[1] != white[2] {
		t.Fatalf("diffuse white %v", white)
	}
	if !(white[0] < twice[0] && twice[0] < peak[0] && peak[0] < 255) {
		t.Fatalf("shoulder white %v 2x %v 5x %v", white, twice, peak)
	}
	// Bright red keeps its hue: green and blue stay zero.
	if px := captureSDR([3]float64{4, 0, 0}); px[1] != 0 || px[2] != 0 || px[0] <= captureSDR([3]float64{0.9, 0, 0})[0] {
		t.Fatalf("bright red %v", px)
	}
	// A BT.2020 red outside BT.709 (negative green and blue) is mixed
	// toward its luminance until the most negative component (green)
	// is zero; blue rises a little above zero, red stays dominant.
	in := [3]float64{1.2, -0.1, -0.05}
	px := captureSDR(in)
	if px[1] != 0 || px[2] == 0 || px[2] > 60 || px[0] < 200 {
		t.Fatalf("out-of-gamut red %v", px)
	}
	// The desaturation keeps luminance: the sRGB-decoded result has the
	// input's Y (0.18) within rounding.
	dec := func(b uint8) float64 {
		v := float64(b) / 255
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	if y := 0.2126*dec(px[0]) + 0.7152*dec(px[1]) + 0.0722*dec(px[2]); math.Abs(y-0.18) > 0.01 {
		t.Fatalf("luminance %.3f after gamut reduction of %v", y, px)
	}
	if nan := captureSDR([3]float64{math.NaN(), math.Inf(1), -1}); nan[0] != 0 || nan[1] < 250 {
		t.Fatalf("NaN/Inf %v", nan)
	}
}

// The lease object of a slot is reused: a steady state of captures
// allocates only the sync file, and a stale handle reports invalid.
func TestCaptureLeaseReused(t *testing.T) {
	r, err := New(8, 8)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	if err := render(r, ports.Scene{Background: "#0000ff"}, nil); err != nil {
		t.Fatal(err)
	}
	first := captureWait(t, r)
	r.EndCapture(first)
	second := captureWait(t, r)
	if second != first {
		t.Fatal("lease object not reused")
	}
	r.EndCapture(second)
	cycle := func() {
		cf, err := r.BeginCapture()
		if err != nil {
			t.Fatal(err)
		}
		if done := cf.Done(); done != nil {
			if err := syncfile.Wait(context.Background(), done); err != nil {
				t.Fatal(err)
			}
		}
		r.EndCapture(cf)
	}
	cycle()
	// No lease wrapper: the allocations left are the Vulkan structs the
	// FFI boundary keeps on the heap (as in Render, budget 16) plus the
	// sync file (os.NewFile and its finalizer).
	if allocs := testing.AllocsPerRun(10, cycle); allocs > 16 {
		t.Errorf("%.1f allocations per capture, want <= 16", allocs)
	}
}

// A failed sync file export fails the capture without waiting: the slot
// stays pending, its semaphore is kept until the fence polls signalled,
// and only then replaced.
func TestCaptureExportFailureKeepsSlotPending(t *testing.T) {
	r, err := New(8, 8)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	if err := render(r, ports.Scene{Background: "#00ff00"}, nil); err != nil {
		t.Fatal(err)
	}
	m := newMockcaptureSync(t)
	r.sync = m
	dev := deviceSync{r}
	// Slots are created on this first capture: their semaphores too.
	m.EXPECT().newSemaphore().RunAndReturn(dev.newSemaphore).Times(captureSlots)
	m.EXPECT().exportSyncFD(mock.Anything).Return(-1, vk.ErrorOutOfHostMemory).Once()
	if _, err := r.BeginCapture(); err == nil || errors.Is(err, ports.ErrCaptureBusy) {
		t.Fatalf("export failure: %v", err)
	}
	s := r.captures[0]
	old := s.done
	if !s.pending || s.leased || !s.replaceDone || s.done == 0 {
		t.Fatalf("slot after export failure: leased=%v pending=%v replace=%v", s.leased, s.pending, s.replaceDone)
	}
	// While the copy runs the slot is skipped and its semaphore is not
	// touched (no destroy expected yet).
	m.EXPECT().fenceStatus(s.fence).Return(vk.Timeout).Once()
	m.EXPECT().exportSyncFD(mock.Anything).RunAndReturn(dev.exportSyncFD).Once()
	second := captureWait(t, r) // the other slot
	if second.(*captureFrame).slot == s {
		t.Fatal("pending slot reused")
	}
	r.EndCapture(second)
	// Once the fence polls signalled the semaphore is replaced (the old
	// one destroyed only now) and the slot works again.
	if err := checked("vkWaitForFences", r.dd.WaitForFences(r.device, 1, &s.fence, 1, math.MaxUint64)); err != nil {
		t.Fatal(err)
	}
	m.EXPECT().fenceStatus(s.fence).RunAndReturn(dev.fenceStatus).Once()
	m.EXPECT().resetFence(s.fence).RunAndReturn(dev.resetFence).Once()
	m.EXPECT().destroySemaphore(old).Run(dev.destroySemaphore).Once()
	boom := errors.New("replacement failed")
	m.EXPECT().newSemaphore().Return(vk.Semaphore(0), boom).Once()
	if _, err := r.BeginCapture(); !errors.Is(err, boom) {
		t.Fatalf("semaphore replacement error: %v", err)
	}
	if s.pending || !s.replaceDone || s.done != 0 {
		t.Fatal("failed replacement lost retry state")
	}
	// Retry after the fence was already reset. No second destruction or
	// fence poll is needed, and no null semaphore may be submitted.
	m.EXPECT().newSemaphore().RunAndReturn(dev.newSemaphore).Once()
	m.EXPECT().exportSyncFD(mock.Anything).RunAndReturn(dev.exportSyncFD).Once()
	third := captureWait(t, r)
	if third.(*captureFrame).slot != s || s.replaceDone {
		t.Fatal("recovered slot not reused")
	}
	r.EndCapture(third)
	// Close destroys both semaphores.
	m.EXPECT().destroySemaphore(mock.Anything).Run(dev.destroySemaphore).Times(captureSlots)
}

// A device error from the fence poll or reset propagates instead of
// being taken as busy.
func TestCapturePollErrorPropagates(t *testing.T) {
	r, err := New(8, 8)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	if err := render(r, ports.Scene{Background: "#00ff00"}, nil); err != nil {
		t.Fatal(err)
	}
	a := captureWait(t, r)
	r.EndCapture(a)
	m := newMockcaptureSync(t)
	r.sync = m
	m.EXPECT().fenceStatus(mock.Anything).Return(vk.ErrorDeviceLost).Once()
	if _, err := r.BeginCapture(); err == nil || errors.Is(err, ports.ErrCaptureBusy) {
		t.Fatalf("poll error: %v", err)
	}
	m.EXPECT().fenceStatus(mock.Anything).Return(vk.Success).Once()
	m.EXPECT().resetFence(mock.Anything).Return(vk.ErrorOutOfDeviceMemory).Once()
	if _, err := r.BeginCapture(); err == nil || errors.Is(err, ports.ErrCaptureBusy) {
		t.Fatalf("reset error: %v", err)
	}
	// Close destroys the semaphores through the seam.
	m.EXPECT().destroySemaphore(mock.Anything).Run(deviceSync{r}.destroySemaphore).Times(captureSlots)
}

// An fp16 client with NaN, +Inf and negative texels: the capture pass
// maps NaN to black, bounds infinity to the shoulder's ceiling and lifts
// the negative one to black, on the GPU as in captureSDR.
func TestCaptureHDRNaNInf(t *testing.T) {
	r := hdrTestRenderer(t)
	format := ports.DMABufFormat{Format: fourcc('A', 'B', '4', 'H'), Modifier: 0}
	if !slices.Contains(r.DMABuf().Formats, format) {
		t.Skip("ABGR16161616F linear import unavailable")
	}
	// x<20: NaN; 20..40: +Inf; 40..: -2.0. Alpha 1.
	texel := func(x int) uint16 {
		switch {
		case x < 20:
			return 0x7e00
		case x < 40:
			return 0x7c00
		}
		return 0xc000
	}
	f := udmabuf16(t, 64, 16, texel)
	buf := &ports.DMABuf{ID: 165, Width: 64, Height: 16, Format: format.Format, Planes: []ports.DMABufPlane{{File: f, Stride: 512}}}
	c := ports.SurfaceContent{ID: 1, Width: 64, Height: 16, Opaque: true, DMABuf: buf, Color: ports.SurfaceColor{TF: ports.ColorTFExtendedLinear, Primaries: ports.ColorPrimariesSRGB}}
	scene := ports.Scene{Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 16}}}}
	if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: c}); err != nil {
		t.Fatal(err)
	}
	img := readPixels(t, r)
	for _, tc := range []struct {
		x    int
		want [3]uint8
	}{
		{5, captureSDR([3]float64{math.NaN(), math.NaN(), math.NaN()})},
		{25, captureSDR([3]float64{math.Inf(1), math.Inf(1), math.Inf(1)})},
		{45, captureSDR([3]float64{-2, -2, -2})},
	} {
		got := img.RGBAAt(tc.x, 8)
		if !near(got, color.RGBA{tc.want[0], tc.want[1], tc.want[2], 255}, 2) {
			t.Errorf("x=%d got %v want %v", tc.x, got, tc.want)
		}
	}
	if img.RGBAAt(5, 8) != (color.RGBA{0, 0, 0, 255}) || img.RGBAAt(45, 8) != (color.RGBA{0, 0, 0, 255}) {
		t.Errorf("NaN %v / negative %v not black", img.RGBAAt(5, 8), img.RGBAAt(45, 8))
	}
	if inf := img.RGBAAt(25, 8); inf.R != 255 {
		// The shoulder's ceiling rounds to 255 at infinity.
		t.Errorf("+Inf %v", inf)
	}
}

// udmabuf16 is a linear RGBA16F client buffer whose RGB texels are v(x).
func udmabuf16(t *testing.T, w, h int, v func(x int) uint16) *os.File {
	t.Helper()
	pixels := make([]byte, w*h*8)
	for y := range h {
		for x := range w {
			i := (y*w + x) * 8
			for ch := range 3 {
				binary.LittleEndian.PutUint16(pixels[i+ch*2:], v(x))
			}
			binary.LittleEndian.PutUint16(pixels[i+6:], 0x3c00)
		}
	}
	mem, err := unix.MemfdCreate("fp16-client", unix.MFD_ALLOW_SEALING)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(mem)
	if err := unix.Ftruncate(mem, int64(len(pixels))); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.Pwrite(mem, pixels, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.FcntlInt(uintptr(mem), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK); err != nil {
		t.Fatal(err)
	}
	dev, err := os.OpenFile("/dev/udmabuf", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("udmabuf unavailable: %v", err)
	}
	defer dev.Close()
	arg := struct {
		memfd, flags uint32
		offset, size uint64
	}{memfd: uint32(mem), flags: 1, size: uint64(len(pixels))}
	fd, _, errno := unix.Syscall(unix.SYS_IOCTL, dev.Fd(), 0x40187542, uintptr(unsafe.Pointer(&arg)))
	if errno != 0 {
		t.Skipf("udmabuf create: %v", errno)
	}
	f := os.NewFile(fd, "fp16-client")
	t.Cleanup(func() { f.Close() })
	return f
}

// Toggling HDR while a capture is leased: the lease's mapping stays
// readable, the next captures follow the new mode, and a solid colour
// round-trips SDR → HDR → SDR within the capture curve's tolerance.
func TestCaptureSurvivesHDRToggle(t *testing.T) {
	r := hdrTestRenderer(t) // HDR, exported target
	scene := ports.Scene{Seq: 1, Background: "#c08040"}
	if err := render(r, scene, nil); err != nil {
		t.Fatal(err)
	}
	hdrShot := readPixels(t, r).RGBAAt(3, 3)
	leased := captureWait(t, r) // HDR capture held across the toggle
	r.SetHDR(0)
	if _, err := r.ExportTargets(0, nil); err != nil {
		t.Fatal(err)
	}
	scene.Seq++
	if err := render(r, scene, nil); err != nil {
		t.Fatal(err)
	}
	sdrShot := readPixels(t, r).RGBAAt(3, 3)
	dst := make([]byte, 4)
	if err := leased.Read(image.Rect(3, 3, 4, 4), dst, 4); err != nil {
		t.Fatal(err)
	}
	if got := (color.RGBA{dst[2], dst[1], dst[0], 255}); got != hdrShot {
		t.Fatalf("leased HDR capture changed by the toggle: %v vs %v", got, hdrShot)
	}
	r.EndCapture(leased)
	// The SDR frame is exact; the HDR capture of the same colour is
	// within the curve's rounding (coverage of the transition, not a
	// claim about the curve's look).
	if sdrShot != (color.RGBA{0xc0, 0x80, 0x40, 255}) {
		t.Fatalf("SDR %v", sdrShot)
	}
	if !near(hdrShot, sdrShot, 10) {
		t.Fatalf("HDR capture %v vs SDR %v", hdrShot, sdrShot)
	}
	// Back to HDR: the capture pass and slots are reused.
	r.SetHDR(203)
	if _, err := r.ExportTargets(1, r.hdrMods); err != nil {
		t.Skipf("no HDR target: %v", err)
	}
	scene.Seq++
	if err := render(r, scene, nil); err != nil {
		t.Fatal(err)
	}
	if again := readPixels(t, r).RGBAAt(3, 3); again != hdrShot {
		t.Fatalf("HDR capture after the round trip %v, before %v", again, hdrShot)
	}
}
