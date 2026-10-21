package drm

import (
	"errors"
	"os"
	"testing"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// gbmBuf is a dmabuf over a closed pipe, as an image buffer hands out.
func gbmBuf(planes int) ports.DMABuf {
	b := ports.DMABuf{Modifier: tiledMod}
	for range planes {
		f, w, _ := os.Pipe()
		w.Close()
		b.Planes = append(b.Planes, ports.DMABufPlane{File: f})
	}
	return b
}

// gbmImages makes an allocator that hands out two images; each image
// closes exactly once.
func gbmImages(t *testing.T, planes int) (imageAllocator, *int) {
	a := newMockimageAllocator(t)
	closed := new(int)
	a.EXPECT().create(200, 100, uint32(fourccXRGB), []uint64{tiledMod}).RunAndReturn(func(int, int, uint32, []uint64) (imageBuffer, error) {
		img := newMockimageBuffer(t)
		img.EXPECT().dmabuf().RunAndReturn(func() (ports.DMABuf, error) { return gbmBuf(planes), nil })
		img.EXPECT().close().Run(func() { *closed++ }).Once()
		return img, nil
	}).Twice()
	a.EXPECT().close().Return().Maybe()
	return a, closed
}

func gbmRenderer(t *testing.T) *portsmocks.MockRenderer {
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().UseTarget(mock.Anything).Return().Maybe()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	return r
}

// GBM images come first: the renderer imports them, the framebuffers are
// made from them, and no image is exported.
func TestShowImagesAllocatesWithGBMFirst(t *testing.T) {
	o, k, _ := testOutput(t)
	o.cursor = nil
	a, closed := gbmImages(t, 1)
	o.openAlloc = func() (imageAllocator, error) { return a, nil }
	r := gbmRenderer(t)
	r.EXPECT().TargetModifiers([]uint64(nil), false).Return([]uint64{tiledMod}).Once()
	r.EXPECT().ImportTargets(mock.MatchedBy(func(b []ports.DMABuf) bool { return len(b) == 2 && b[0].Modifier == tiledMod })).Return(nil).Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Twice()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil)
	k.EXPECT().rmFB(mock.Anything).Return(nil)
	if err := o.showImages(r, imagesDriver, nil); err != nil {
		t.Fatal(err)
	}
	if o.imageAlloc != allocGBM || o.kind != imagesDriver || o.imageMod != tiledMod || len(o.images) != 2 || *closed != 0 {
		t.Fatalf("alloc %q kind %d modifier %#x images %d closed %d", o.imageAlloc, o.kind, o.imageMod, len(o.images), *closed)
	}
	o.freeImages()
	if *closed != 2 || o.images != nil {
		t.Fatalf("closed %d images, %d left", *closed, len(o.images))
	}
}

// The plane's modifiers reach the renderer with the single-plane rule, and
// a GBM failure falls back to the renderer's own images of the same kind.
func TestShowImagesFallsBackToVulkanWhenGBMFails(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(t *testing.T, o *Output, r *portsmocks.MockRenderer)
	}{
		"gbm unavailable": {func(t *testing.T, o *Output, r *portsmocks.MockRenderer) {
			o.openAlloc = func() (imageAllocator, error) { return nil, errors.New("no libgbm") }
		}},
		"no common modifier": {func(t *testing.T, o *Output, r *portsmocks.MockRenderer) {
			a := newMockimageAllocator(t)
			a.EXPECT().close().Return().Maybe()
			o.openAlloc = func() (imageAllocator, error) { return a, nil }
			r.EXPECT().TargetModifiers(mock.Anything, false).Return(nil).Once()
		}},
		"allocation fails": {func(t *testing.T, o *Output, r *portsmocks.MockRenderer) {
			a := newMockimageAllocator(t)
			a.EXPECT().close().Return().Maybe()
			a.EXPECT().create(200, 100, uint32(fourccXRGB), []uint64{tiledMod}).Return(nil, errors.New("gbm: no buffer")).Once()
			o.openAlloc = func() (imageAllocator, error) { return a, nil }
			r.EXPECT().TargetModifiers(mock.Anything, false).Return([]uint64{tiledMod}).Once()
		}},
		"second allocation fails": {func(t *testing.T, o *Output, r *portsmocks.MockRenderer) {
			a := newMockimageAllocator(t)
			a.EXPECT().close().Return().Maybe()
			img := newMockimageBuffer(t)
			img.EXPECT().dmabuf().Return(gbmBuf(1), nil).Once()
			img.EXPECT().close().Return().Once() // freed with the failed attempt
			a.EXPECT().create(200, 100, uint32(fourccXRGB), []uint64{tiledMod}).Return(img, nil).Once()
			a.EXPECT().create(200, 100, uint32(fourccXRGB), []uint64{tiledMod}).Return(nil, errors.New("out of memory")).Once()
			o.openAlloc = func() (imageAllocator, error) { return a, nil }
			r.EXPECT().TargetModifiers(mock.Anything, false).Return([]uint64{tiledMod}).Once()
		}},
		"import refused": {func(t *testing.T, o *Output, r *portsmocks.MockRenderer) {
			a, closed := gbmImages(t, 1)
			_ = closed
			o.openAlloc = func() (imageAllocator, error) { return a, nil }
			r.EXPECT().TargetModifiers(mock.Anything, false).Return([]uint64{tiledMod}).Once()
			r.EXPECT().ImportTargets(mock.Anything).Return(errors.New("explicit layout refused")).Once()
		}},
		"framebuffer refused": {func(t *testing.T, o *Output, r *portsmocks.MockRenderer) {
			a, _ := gbmImages(t, 1)
			o.openAlloc = func() (imageAllocator, error) { return a, nil }
			r.EXPECT().TargetModifiers(mock.Anything, false).Return([]uint64{tiledMod}).Once()
			r.EXPECT().ImportTargets(mock.Anything).Return(nil).Once()
			r.EXPECT().ImportTargets([]ports.DMABuf(nil)).Return(nil).Once()
			o.k.(*mockkms).EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(0, unix.EINVAL).Once()
		}},
	} {
		t.Run(name, func(t *testing.T) {
			o, k, _ := testOutput(t)
			o.cursor = nil
			r := gbmRenderer(t)
			tc.setup(t, o, r)
			r.EXPECT().ExportTargets(2, []uint64(nil), false).Return([]ports.DMABuf{gbmBuf(1), gbmBuf(1)}, nil).Once()
			k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Maybe()
			k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
			k.EXPECT().createBlob(mock.Anything).Return(99, nil)
			k.EXPECT().destroyBlob(mock.Anything).Return(nil)
			if err := o.showImages(r, imagesDriver, nil); err != nil {
				t.Fatal(err)
			}
			if o.imageAlloc != allocVulkan || o.kind != imagesDriver || len(o.images) != 0 || o.fbs[0] == 0 || o.fbs[1] == 0 {
				t.Fatalf("alloc %q kind %d images %d fbs %v", o.imageAlloc, o.kind, len(o.images), o.fbs)
			}
		})
	}
}

// A failed GBM open is remembered: the next kind and the next setup do not
// open the library again.
func TestGBMOpenFailureIsRemembered(t *testing.T) {
	o, k, _ := testOutput(t, unix.EINVAL)
	o.cursor = nil
	opens := 0
	o.openAlloc = func() (imageAllocator, error) { opens++; return nil, errors.New("no libgbm") }
	r := gbmRenderer(t)
	r.EXPECT().ExportTargets(2, []uint64(nil), false).Return([]ports.DMABuf{gbmBuf(2), gbmBuf(2)}, nil).Once()
	r.EXPECT().ExportTargets(2, []uint64(nil), true).Return([]ports.DMABuf{gbmBuf(1), gbmBuf(1)}, nil).Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil)
	k.EXPECT().rmFB(mock.Anything).Return(nil)
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil)
	if err := o.showImages(r, imagesDriver, nil); err != nil {
		t.Fatal(err)
	}
	if opens != 1 || o.kind != imagesSinglePlane {
		t.Fatalf("%d opens, kind %d", opens, o.kind)
	}
}

// A modeset test that refuses GBM images tries the renderer's images of the
// same kind before the next kind.
func TestShowImagesRefusedGBMImagesTryVulkanOfTheSameKind(t *testing.T) {
	o, k, commits := testOutput(t, unix.EINVAL)
	o.cursor = nil
	a, closed := gbmImages(t, 1)
	o.openAlloc = func() (imageAllocator, error) { return a, nil }
	r := gbmRenderer(t)
	r.EXPECT().TargetModifiers([]uint64(nil), false).Return([]uint64{tiledMod}).Once()
	r.EXPECT().ImportTargets(mock.MatchedBy(func(b []ports.DMABuf) bool { return len(b) == 2 })).Return(nil).Once()
	// The refused GBM images leave no renderer target behind.
	r.EXPECT().ImportTargets([]ports.DMABuf(nil)).Return(nil).Once()
	r.EXPECT().ExportTargets(2, []uint64(nil), false).Return([]ports.DMABuf{gbmBuf(1), gbmBuf(1)}, nil).Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil)
	k.EXPECT().rmFB(mock.Anything).Return(nil)
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil)
	if err := o.showImages(r, imagesDriver, nil); err != nil {
		t.Fatal(err)
	}
	if o.imageAlloc != allocVulkan || o.kind != imagesDriver || *closed != 2 || len(o.images) != 0 || len(*commits) != 3 {
		t.Fatalf("alloc %q kind %d closed %d images %d commits %d", o.imageAlloc, o.kind, *closed, len(o.images), len(*commits))
	}
}

// Closing the output frees the GBM images and the allocator.
func TestOutputFreeImagesClosesGBMImages(t *testing.T) {
	o, k, _ := testOutput(t)
	img := newMockimageBuffer(t)
	img.EXPECT().close().Return().Twice()
	o.images = []imageBuffer{img, img}
	o.fbs = [2]uint32{70, 71}
	k.EXPECT().rmFB(mock.Anything).Return(nil).Twice()
	o.freeImages()
	if o.images != nil || o.fbs != [2]uint32{} {
		t.Fatalf("images %v fbs %v", o.images, o.fbs)
	}
}

// A setup that finds images left over frees them first, with the renderer's
// targets over GBM ones, and a refused modeset of GBM images (no fallback
// left) drops the targets with the buffers.
func TestSetupStartsFromCleanState(t *testing.T) {
	o, k, _ := testOutput(t)
	o.cursor = nil
	img := newMockimageBuffer(t)
	img.EXPECT().close().Return().Once()
	o.images = []imageBuffer{img}
	o.fbs = [2]uint32{70, 71}
	k.EXPECT().rmFB(uint32(70)).Return(nil).Once()
	k.EXPECT().rmFB(uint32(71)).Return(nil).Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(80, nil)
	r := gbmRenderer(t)
	r.EXPECT().ImportTargets([]ports.DMABuf(nil)).Return(nil).Once()
	r.EXPECT().ExportTargets(2, []uint64(nil), false).Return([]ports.DMABuf{gbmBuf(1), gbmBuf(1)}, nil).Once()
	if err := o.exportImages(r, nil, false); err != nil {
		t.Fatal(err)
	}
	if o.images != nil || o.fbs != [2]uint32{80, 80} {
		t.Fatalf("images %v fbs %v", o.images, o.fbs)
	}
}

// Without CPU fence waits every fence is ready; with them only a signalled
// client fence is (a pipe stands in: readable means signalled).
func TestClientFenceReady(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	o, _, _ := testOutput(t)
	if !o.clientFenceReady(r) || !o.clientFenceReady(nil) {
		t.Fatal("fence not ready without CPU waits")
	}
	o.cpuFences = true
	if !o.clientFenceReady(nil) {
		t.Fatal("no fence must be ready")
	}
	if o.clientFenceReady(r) {
		t.Fatal("pending client fence ready")
	}
	_, _ = w.Write([]byte{1})
	if !o.clientFenceReady(r) {
		t.Fatal("signalled client fence not ready")
	}
}

// With CPU waits a client buffer whose fence is pending is composed, not
// scanned out and not put on the overlay: neither plane waits for it. Once
// the fence signalled the same frame scans out.
func TestPendingClientFenceComposes(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	o, k, _ := testOutput(t)
	k.EXPECT().addFB(mock.Anything, mock.Anything).Return(60, nil).Maybe()
	o.cpuFences = true
	o.primary.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	s := ports.Scene{OutputWidth: 200, OutputHeight: 100, Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 200, H: 100}, Fullscreen: true}}}
	c := ports.SurfaceContent{ID: 1, Width: 200, Height: 100, LogicalW: 200, LogicalH: 100, Opaque: true, Acquire: r, DMABuf: &ports.DMABuf{ID: 5, Format: fourccXRGB, Width: 200, Height: 100, Planes: []ports.DMABufPlane{{File: r}}}}
	contents := map[ports.WindowID]ports.SurfaceContent{1: c}
	if fb, _, _ := o.scanoutFrame(s, contents); fb != 0 || o.reason != "fence_pending" {
		t.Fatalf("pending fence: fb %d reason %q", fb, o.reason)
	}
	_, _ = w.Write([]byte{1})
	if fb, _, _ := o.scanoutFrame(s, contents); fb == 0 {
		t.Fatalf("signalled fence: reason %q", o.reason)
	}
}
