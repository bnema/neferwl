package drm

import (
	"bytes"
	"errors"
	"os"
	"testing"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

const tSide = 16

// dumbOK expects two dumb buffers with handles 10 and 11, mappings (with
// pitch bytes per row) returned in mem, and framebuffers 90 and 91.
func dumbOK(k *mockkms, pitch int) (mem [2][]byte) {
	for i := range mem {
		mem[i] = make([]byte, pitch*tSide)
		h := uint32(10 + i)
		b := dumbBuffer{handle: h, pitch: uint32(pitch), size: uint64(len(mem[i]))}
		k.EXPECT().createDumb(tSide, tSide).Return(b, nil).Once()
		k.EXPECT().mapDumb(b).Return(mem[i], nil).Once()
		k.EXPECT().addFBHandle(b, tSide, tSide, uint32(fourccARGB)).Return(90+uint32(i), nil).Once()
	}
	return mem
}

func dumbFree(k *mockkms) {
	for i := range 2 {
		k.EXPECT().rmFB(90 + uint32(i)).Return(nil).Once()
		k.EXPECT().destroyDumb(10 + uint32(i)).Return(nil).Once()
	}
	k.EXPECT().unmapDumb(mock.Anything).Return(nil).Twice()
}

func rendererBufs() [2]ports.DMABuf {
	var b [2]ports.DMABuf
	for i := range b {
		f, w, _ := os.Pipe()
		w.Close()
		b[i] = ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	return b
}

func rendererOK(k *mockkms, r *portsmocks.MockRenderer) {
	r.EXPECT().CursorBuffers(tSide).Return(rendererBufs(), nil).Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccARGB)).Return(80, nil).Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccARGB)).Return(81, nil).Once()
}

// Dumb buffers come first: the renderer allocates nothing.
func TestCursorSetupUsesDumbBuffers(t *testing.T) {
	k, r := newMockkms(t), portsmocks.NewMockRenderer(t)
	dumbOK(k, tSide*4)
	c := newCursor(&plane{}, tSide)
	dumbErr, err := c.setup(k, r)
	if dumbErr != nil || err != nil {
		t.Fatalf("setup: %v %v", dumbErr, err)
	}
	if c.alloc != allocDumb || c.fbs != [2]uint32{90, 91} {
		t.Fatalf("alloc %q fbs %v", c.alloc, c.fbs)
	}
	dumbFree(k)
	c.free()
	if c.imgs != nil || c.fbs != [2]uint32{} {
		t.Fatal("images kept after free")
	}
}

// A dumb buffer that cannot be made (or mapped, or wrapped) falls back to
// the renderer's images, and what was made is released.
func TestCursorSetupFallsBackToRenderer(t *testing.T) {
	for name, fail := range map[string]func(k *mockkms){
		"create": func(k *mockkms) {
			k.EXPECT().createDumb(tSide, tSide).Return(dumbBuffer{}, unix.ENOSYS).Once()
		},
		"map": func(k *mockkms) {
			b := dumbBuffer{handle: 10, pitch: tSide * 4, size: tSide * tSide * 4}
			k.EXPECT().createDumb(tSide, tSide).Return(b, nil).Once()
			k.EXPECT().mapDumb(b).Return(nil, unix.ENOMEM).Once()
			k.EXPECT().destroyDumb(uint32(10)).Return(nil).Once()
		},
		"framebuffer": func(k *mockkms) {
			b := dumbBuffer{handle: 10, pitch: tSide * 4, size: tSide * tSide * 4}
			mem := make([]byte, b.size)
			k.EXPECT().createDumb(tSide, tSide).Return(b, nil).Once()
			k.EXPECT().mapDumb(b).Return(mem, nil).Once()
			k.EXPECT().addFBHandle(b, tSide, tSide, uint32(fourccARGB)).Return(0, unix.EINVAL).Once()
			k.EXPECT().unmapDumb(mem).Return(nil).Once()
			k.EXPECT().destroyDumb(uint32(10)).Return(nil).Once()
		},
		"second buffer": func(k *mockkms) {
			b := dumbBuffer{handle: 10, pitch: tSide * 4, size: tSide * tSide * 4}
			mem := make([]byte, b.size)
			k.EXPECT().createDumb(tSide, tSide).Return(b, nil).Once()
			k.EXPECT().mapDumb(b).Return(mem, nil).Once()
			k.EXPECT().addFBHandle(b, tSide, tSide, uint32(fourccARGB)).Return(90, nil).Once()
			k.EXPECT().createDumb(tSide, tSide).Return(dumbBuffer{}, unix.EMFILE).Once()
			k.EXPECT().rmFB(uint32(90)).Return(nil).Once()
			k.EXPECT().unmapDumb(mem).Return(nil).Once()
			k.EXPECT().destroyDumb(uint32(10)).Return(nil).Once()
		},
	} {
		t.Run(name, func(t *testing.T) {
			k, r := newMockkms(t), portsmocks.NewMockRenderer(t)
			fail(k)
			rendererOK(k, r)
			c := newCursor(&plane{}, tSide)
			dumbErr, err := c.setup(k, r)
			if dumbErr == nil || err != nil {
				t.Fatalf("setup: %v %v", dumbErr, err)
			}
			if c.alloc != allocVulkan || c.fbs != [2]uint32{80, 81} {
				t.Fatalf("alloc %q fbs %v", c.alloc, c.fbs)
			}
		})
	}
}

// Without either allocator there are no images.
func TestCursorSetupFailsWithoutAllocators(t *testing.T) {
	k, r := newMockkms(t), portsmocks.NewMockRenderer(t)
	k.EXPECT().createDumb(tSide, tSide).Return(dumbBuffer{}, unix.ENOSYS).Once()
	r.EXPECT().CursorBuffers(tSide).Return([2]ports.DMABuf{}, errors.New("no export")).Once()
	c := newCursor(&plane{}, tSide)
	dumbErr, err := c.setup(k, r)
	if dumbErr == nil || err == nil || c.imgs != nil || c.fbs != [2]uint32{} {
		t.Fatalf("setup: %v %v imgs %v fbs %v", dumbErr, err, c.imgs, c.fbs)
	}
	if _, err := c.useFallback(0); err != nil {
		t.Fatal(err)
	}
}

// A renderer image whose framebuffer fails is released.
func TestRendererCursorReleasesOnFailure(t *testing.T) {
	k, r := newMockkms(t), portsmocks.NewMockRenderer(t)
	r.EXPECT().CursorBuffers(tSide).Return(rendererBufs(), nil).Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccARGB)).Return(80, nil).Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccARGB)).Return(0, unix.EINVAL).Once()
	k.EXPECT().rmFB(uint32(80)).Return(nil).Once()
	if _, err := newRendererCursor(k, r, tSide); err == nil {
		t.Fatal("framebuffer failure not reported")
	}
}

// cursorOutput is a test output whose cursor plane has size tSide.
func cursorOutput(t *testing.T, errs ...error) (*Output, *mockkms, *portsmocks.MockRenderer, *[]commitRec) {
	o, k, commits := testOutput(t, errs...)
	o.cursor = newCursor(&plane{id: tCursor, typ: planeCursor, props: planeProps}, tSide)
	return o, k, portsmocks.NewMockRenderer(t), commits
}

// The plane refuses the dumb images: the renderer's are tried (TEST_ONLY),
// the image on the cursor is rewritten into them, and writes go there.
func TestCursorDumbRefusedUsesRendererImages(t *testing.T) {
	o, k, r, commits := cursorOutput(t, unix.EINVAL)
	dumbOK(k, tSide*4)
	if _, err := o.cursor.setup(k, r); err != nil {
		t.Fatal(err)
	}
	px := []byte{1, 2, 3, 4}
	if err := o.cursor.setImage(px, 1, 1, 2, 3, 0); err != nil {
		t.Fatal(err)
	}
	dumbFree(k)
	rendererOK(k, r)
	var written []int
	r.EXPECT().WriteCursor(mock.Anything, px, 1, 1).RunAndReturn(func(i int, _ []byte, _, _ int) error {
		written = append(written, i)
		return nil
	})
	o.testCursor()
	c := o.cursor
	if c.off || c.alloc != allocVulkan || c.fbs != [2]uint32{80, 81} || !c.image || c.hotX != 2 || c.hotY != 3 {
		t.Fatalf("off %v alloc %q fbs %v image %v hot %d,%d", c.off, c.alloc, c.fbs, c.image, c.hotX, c.hotY)
	}
	if len(written) != 1 || len(*commits) != 2 {
		t.Fatalf("written %v, %d TEST_ONLY commits", written, len(*commits))
	}
	if v, _ := (*commits)[1].req.value(tCursor, pFB); v != 80 || (*commits)[1].flags != atomicTestOnly {
		t.Fatalf("second test shows fb %d flags %#x", v, (*commits)[1].flags)
	}
	// Further changes are written by the renderer, not into the freed buffers.
	if err := c.setImage(px, 1, 1, 0, 0, 0); err != nil || len(written) != 2 {
		t.Fatalf("write after fallback: %v %v", err, written)
	}
}

// Both kinds refused: the software cursor takes over.
func TestCursorBothRefusedGoesOff(t *testing.T) {
	o, k, r, commits := cursorOutput(t, unix.EINVAL, unix.EINVAL)
	dumbOK(k, tSide*4)
	if _, err := o.cursor.setup(k, r); err != nil {
		t.Fatal(err)
	}
	dumbFree(k)
	rendererOK(k, r)
	if !o.testCursor() {
		t.Fatal("no change reported")
	}
	if !o.cursor.off || o.cursor.alloc != allocVulkan || len(*commits) != 2 {
		t.Fatalf("off %v alloc %q commits %d", o.cursor.off, o.cursor.alloc, len(*commits))
	}
	if o.cursor.desired().on {
		t.Fatal("cursor shown while off")
	}
	// The renderer's images are released with the output.
	k.EXPECT().rmFB(uint32(80)).Return(nil).Once()
	k.EXPECT().rmFB(uint32(81)).Return(nil).Once()
	o.cursor.free()
}

// Dumb images the plane takes stay, with no second test.
func TestCursorDumbAcceptedStays(t *testing.T) {
	o, k, r, commits := cursorOutput(t)
	dumbOK(k, tSide*4)
	if _, err := o.cursor.setup(k, r); err != nil {
		t.Fatal(err)
	}
	if o.testCursor() || o.cursor.off || o.cursor.alloc != allocDumb || len(*commits) != 1 {
		t.Fatalf("off %v alloc %q commits %d", o.cursor.off, o.cursor.alloc, len(*commits))
	}
	// An error other than a refusal changes nothing.
	dumbFree(k)
	o.cursor.free()
}

// Renderer images refused while no dumb ones exist: off, no retry.
func TestCursorRendererRefusedGoesOff(t *testing.T) {
	o, k, r, commits := cursorOutput(t, unix.EINVAL)
	k.EXPECT().createDumb(tSide, tSide).Return(dumbBuffer{}, unix.ENOSYS).Once()
	rendererOK(k, r)
	if _, err := o.cursor.setup(k, r); err != nil {
		t.Fatal(err)
	}
	if !o.testCursor() || !o.cursor.off || len(*commits) != 1 {
		t.Fatalf("off %v commits %d", o.cursor.off, len(*commits))
	}
}

// Writes go to the images that own the cursor, and the output's HDR white
// is passed along.
func TestCursorWritesGoToCurrentImages(t *testing.T) {
	o, _, _, _ := cursorOutput(t)
	imgs := newMockcursorImages(t)
	imgs.EXPECT().fb(0).Return(90).Once()
	imgs.EXPECT().fb(1).Return(91).Once()
	o.cursor.install(imgs, allocDumb)
	px := []byte{1, 2, 3, 4}
	imgs.EXPECT().write(0, px, 1, 1, float64(0)).Return(nil).Once()
	if err := o.cursor.setImage(px, 1, 1, 0, 0, o.cursorNits()); err != nil {
		t.Fatal(err)
	}
	o.hdr.on, o.hdr.settings.SDRBrightness = true, 203
	if got := o.cursorNits(); got != 203 {
		t.Fatalf("HDR white %v", got)
	}
	o.cursor.screen = 90
	imgs.EXPECT().write(1, px, 1, 1, float64(203)).Return(errors.New("full")).Once()
	if err := o.cursor.setImage(px, 1, 1, 0, 0, o.cursorNits()); err == nil {
		t.Fatal("write error swallowed")
	}
}

// Dumb pitch may exceed the row size: rows land at the pitch, the image is
// cropped to the side and cleared around.
func TestDumbCursorWritePitchCropClear(t *testing.T) {
	k := newMockkms(t)
	const pitch = tSide*4 + 32
	mem := dumbOK(k, pitch)
	d, err := newDumbCursor(k, tSide)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { dumbFree(k); d.close() }()
	big := bytes.Repeat([]byte{0xff}, 40*40*4)
	if err := d.write(1, big, 40, 40, 0); err != nil { // cropped to tSide
		t.Fatal(err)
	}
	last := (tSide - 1) * pitch
	if m := mem[1]; m[last+(tSide-1)*4] != 0xff || m[last+tSide*4] != 0 || m[pitch-1] != 0 {
		t.Fatal("cropped image misplaced at the pitch")
	}
	if err := d.write(1, []byte{1, 2, 3, 4}, 1, 1, 0); err != nil {
		t.Fatal(err)
	}
	m := mem[1]
	if !bytes.Equal(m[:4], []byte{1, 2, 3, 4}) || m[4] != 0 || m[pitch] != 0 || m[last+(tSide-1)*4] != 0 {
		t.Fatal("image not cleared around the new pixels")
	}
	// A wide, short image keeps its rows apart.
	row := []byte{9, 9, 9, 9, 8, 8, 8, 8}
	if err := d.write(0, append(row, row...), 2, 2, 0); err != nil {
		t.Fatal(err)
	}
	if m := mem[0]; !bytes.Equal(m[:8], row) || !bytes.Equal(m[pitch:pitch+8], row) || m[8] != 0 {
		t.Fatal("rows of a narrow image")
	}
	if err := d.write(0, []byte{1}, 1, 1, 0); err == nil {
		t.Fatal("short image accepted")
	}
	if err := d.write(2, nil, 0, 0, 0); err == nil {
		t.Fatal("bad index accepted")
	}
}

// HDR cursors convert exactly as the renderer did (see hdrcursor's vector).
func TestDumbCursorWriteHDR(t *testing.T) {
	k := newMockkms(t)
	mem := dumbOK(k, tSide*4)
	d, err := newDumbCursor(k, tSide)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { dumbFree(k); d.close() }()
	if err := d.write(0, []byte{255, 255, 255, 255}, 1, 1, 203); err != nil {
		t.Fatal(err)
	}
	if m := mem[0]; m[0] != 148 || m[1] != 148 || m[2] != 148 || m[3] != 255 {
		t.Fatalf("HDR white: %v", m[:4])
	}
	if err := d.write(1, []byte{255, 255, 255, 255}, 1, 1, 0); err != nil || mem[1][0] != 255 {
		t.Fatalf("SDR white: %v %v", err, mem[1][:4])
	}
	if d.cache.Len() != 4 {
		t.Fatalf("cache %d", d.cache.Len())
	}
}
