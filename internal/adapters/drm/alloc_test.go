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

// twoGBMImages makes an allocator that hands out two images of the given
// plane count; each image closes exactly once, counted in closed.
func twoGBMImages(t *testing.T, planes int) (a imageAllocator, closed *int) {
	m := newMockimageAllocator(t)
	closed = new(int)
	m.EXPECT().create(200, 100, uint32(fourccXRGB), []uint64{tiledMod}).RunAndReturn(func(int, int, uint32, []uint64) (imageBuffer, error) {
		img := newMockimageBuffer(t)
		img.EXPECT().dmabuf().RunAndReturn(func() (ports.DMABuf, error) { return gbmBuf(planes), nil })
		img.EXPECT().close().Run(func() { *closed++ }).Once()
		return img, nil
	}).Twice()
	m.EXPECT().close().Return().Maybe()
	return m, closed
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
	a, closed := twoGBMImages(t, 1)
	o.card = &Card{alloc: a}
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
	if o.kind != imagesDriver || o.imageMod != tiledMod || len(o.images) != 2 || *closed != 0 {
		t.Fatalf("kind %d modifier %#x images %d closed %d", o.kind, o.imageMod, len(o.images), *closed)
	}
	r.EXPECT().ImportTargets([]ports.DMABuf(nil)).Return(nil).Once()
	o.dropImages(r)
	if *closed != 2 || o.images != nil || o.fbs != [2]uint32{} {
		t.Fatalf("closed %d images, %d left, fbs %v", *closed, len(o.images), o.fbs)
	}
}

// A GBM failure at any step falls back to the renderer's own images of the same kind.
func TestShowImagesFallsBackToVulkanWhenGBMFails(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(t *testing.T, o *Output, k *mockkms, r *portsmocks.MockRenderer)
	}{
		"gbm unavailable": {func(_ *testing.T, o *Output, _ *mockkms, _ *portsmocks.MockRenderer) {
			o.card = &Card{}
		}},
		"no common modifier": {func(t *testing.T, o *Output, k *mockkms, r *portsmocks.MockRenderer) {
			a := newMockimageAllocator(t)
			a.EXPECT().close().Return().Maybe()
			o.card = &Card{alloc: a}
			r.EXPECT().TargetModifiers(mock.Anything, false).Return(nil).Once()
		}},
		"allocation fails": {func(t *testing.T, o *Output, k *mockkms, r *portsmocks.MockRenderer) {
			a := newMockimageAllocator(t)
			a.EXPECT().close().Return().Maybe()
			a.EXPECT().create(200, 100, uint32(fourccXRGB), []uint64{tiledMod}).Return(nil, errors.New("gbm: no buffer")).Once()
			o.card = &Card{alloc: a}
			r.EXPECT().TargetModifiers(mock.Anything, false).Return([]uint64{tiledMod}).Once()
		}},
		"second allocation fails": {func(t *testing.T, o *Output, k *mockkms, r *portsmocks.MockRenderer) {
			a := newMockimageAllocator(t)
			a.EXPECT().close().Return().Maybe()
			img := newMockimageBuffer(t)
			img.EXPECT().dmabuf().Return(gbmBuf(1), nil).Once()
			img.EXPECT().close().Return().Once() // freed with the failed attempt
			r.EXPECT().ImportTargets([]ports.DMABuf(nil)).Return(nil).Once()
			a.EXPECT().create(200, 100, uint32(fourccXRGB), []uint64{tiledMod}).Return(img, nil).Once()
			a.EXPECT().create(200, 100, uint32(fourccXRGB), []uint64{tiledMod}).Return(nil, errors.New("out of memory")).Once()
			o.card = &Card{alloc: a}
			r.EXPECT().TargetModifiers(mock.Anything, false).Return([]uint64{tiledMod}).Once()
		}},
		"import refused": {func(t *testing.T, o *Output, k *mockkms, r *portsmocks.MockRenderer) {
			a, _ := twoGBMImages(t, 1)
			o.card = &Card{alloc: a}
			r.EXPECT().TargetModifiers(mock.Anything, false).Return([]uint64{tiledMod}).Once()
			r.EXPECT().ImportTargets(mock.Anything).Return(errors.New("explicit layout refused")).Once()
			// The images made before the refusal go with a renderer drop.
			r.EXPECT().ImportTargets([]ports.DMABuf(nil)).Return(nil).Once()
		}},
		"framebuffer refused": {func(t *testing.T, o *Output, k *mockkms, r *portsmocks.MockRenderer) {
			a, _ := twoGBMImages(t, 1)
			o.card = &Card{alloc: a}
			r.EXPECT().TargetModifiers(mock.Anything, false).Return([]uint64{tiledMod}).Once()
			r.EXPECT().ImportTargets(mock.Anything).Return(nil).Once()
			r.EXPECT().ImportTargets([]ports.DMABuf(nil)).Return(nil).Once()
			k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(0, unix.EINVAL).Once()
		}},
	} {
		t.Run(name, func(t *testing.T) {
			o, k, _ := testOutput(t)
			o.cursor = nil
			r := gbmRenderer(t)
			tc.setup(t, o, k, r)
			r.EXPECT().ExportTargets(2, []uint64(nil), false).Return([]ports.DMABuf{gbmBuf(1), gbmBuf(1)}, nil).Once()
			k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Maybe()
			k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
			k.EXPECT().createBlob(mock.Anything).Return(99, nil)
			k.EXPECT().destroyBlob(mock.Anything).Return(nil)
			if err := o.showImages(r, imagesDriver, nil); err != nil {
				t.Fatal(err)
			}
			// The renderer's own images: no GBM image is alive.
			if o.kind != imagesDriver || len(o.images) != 0 || o.fbs[0] == 0 || o.fbs[1] == 0 {
				t.Fatalf("kind %d images %d fbs %v", o.kind, len(o.images), o.fbs)
			}
		})
	}
}

// A failed GBM open is remembered: the next kind and the next setup do not
// open the library again.
func TestGBMOpenFailureIsRemembered(t *testing.T) {
	o, k, _ := testOutput(t, unix.EINVAL)
	o.cursor = nil
	o.card = &Card{}
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
	if o.kind != imagesSinglePlane {
		t.Fatalf("kind %d", o.kind)
	}
}

// A modeset test that refuses GBM images tries the renderer's images of the
// same kind before the next kind.
func TestShowImagesRefusedGBMImagesTryVulkanOfTheSameKind(t *testing.T) {
	o, k, commits := testOutput(t, unix.EINVAL)
	o.cursor = nil
	a, closed := twoGBMImages(t, 1)
	o.card = &Card{alloc: a}
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
	if o.kind != imagesDriver || *closed != 2 || len(o.images) != 0 || len(*commits) != 3 {
		t.Fatalf("kind %d closed %d images %d commits %d", o.kind, *closed, len(o.images), len(*commits))
	}
}

// Closing the output frees the GBM images and closes the allocator once.
func TestOutputCloseFreesGBMImagesAndAllocator(t *testing.T) {
	o, k, _ := testOutput(t)
	o.cursor = nil
	img := newMockimageBuffer(t)
	img.EXPECT().close().Return().Twice()
	alloc := newMockimageAllocator(t)
	alloc.EXPECT().close().Return().Once()
	o.images = []imageBuffer{img, img}
	o.card = &Card{alloc: alloc}
	o.fbs = [2]uint32{70, 71}
	k.EXPECT().rmFB(uint32(70)).Return(nil).Once()
	k.EXPECT().rmFB(uint32(71)).Return(nil).Once()
	o.Close()
	o.card.Close()
	if o.images != nil || o.card.alloc != nil || o.fbs != [2]uint32{} {
		t.Fatalf("images %v alloc %v fbs %v", o.images, o.card.alloc, o.fbs)
	}
}

// A setup that finds images left over frees them first, with the renderer's
// targets over GBM ones: the renderer never keeps a target over a freed
// image.
func TestSetupStartsFromCleanState(t *testing.T) {
	o, k, _ := testOutput(t)
	o.cursor = nil
	img := newMockimageBuffer(t)
	img.EXPECT().close().Return().Once()
	o.images = []imageBuffer{img}
	o.card = &Card{}
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

func TestCardSharesAllocator(t *testing.T) {
	alloc := newMockimageAllocator(t)
	card := &Card{alloc: alloc}
	closed := 0
	alloc.EXPECT().create(200, 100, uint32(fourccXRGB), []uint64{tiledMod}).RunAndReturn(func(int, int, uint32, []uint64) (imageBuffer, error) {
		img := newMockimageBuffer(t)
		img.EXPECT().dmabuf().RunAndReturn(func() (ports.DMABuf, error) { return gbmBuf(1), nil }).Once()
		img.EXPECT().close().Run(func() { closed++ }).Once()
		return img, nil
	}).Times(4)
	alloc.EXPECT().close().Run(func() {
		if closed != 4 {
			t.Errorf("allocator closed before outputs: %d images released", closed)
		}
	}).Once()
	for output := range 2 {
		o, k, _ := testOutput(t)
		o.card, o.cursor, o.fbs = card, nil, [2]uint32{}
		r := gbmRenderer(t)
		r.EXPECT().TargetModifiers([]uint64(nil), false).Return([]uint64{tiledMod}).Once()
		r.EXPECT().ImportTargets(mock.Anything).Return(nil).Once()
		for image := range 2 {
			id := uint32(100 + output*2 + image)
			k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(id, nil).Once()
			k.EXPECT().rmFB(id).Return(nil).Once()
		}

		if err := o.gbmImages(r, nil, false); err != nil {
			t.Fatal(err)
		}
		o.Close()
		if card.alloc == nil {
			t.Fatal("output closed card allocator")
		}
	}
	card.Close()
	card.Close()
}
