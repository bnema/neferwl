package drm

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// An event queued before a modeset (or for an earlier output on the CRTC)
// must not complete the commit that follows.
func TestStaleEventDoesNotCompleteNewCommit(t *testing.T) {
	o, _, commits := testOutput(t)
	if err := o.commitFrame(70, nil, false, false, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	old := eventOf((*commits)[0])
	o.frame.endPending() // a modeset dropped the commit; its event is queued
	if err := o.commitFrame(71, nil, false, false, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	if o.completed(old, nil) || !o.frame.pendingCommit() {
		t.Fatal("stale event completed the new commit")
	}
	if !o.completed(eventOf((*commits)[1]), nil) || o.frame.pendingCommit() {
		t.Fatal("own event did not complete the commit")
	}
}

// After EBUSY no commit of ours is known: the next event of the CRTC ends
// the wait, then the frame commits.
func TestBusyFrameCommitsAfterEvent(t *testing.T) {
	o, _, commits := testOutput(t, unix.EBUSY)
	enabled := true
	if err := o.commitFrame(70, nil, false, false, pendingFrame{}); !o.commitFailed(err, &enabled) {
		t.Fatalf("EBUSY not handled: %v", err)
	}
	if !o.completed(flipEvent{crtc: tCrtc, user: 12345 << userKindBits}, nil) || o.frame.pendingCommit() {
		t.Fatal("EBUSY wait not ended by the CRTC's event")
	}
	if err := o.commitFrame(70, nil, false, false, pendingFrame{}); err != nil || len(*commits) != 2 || !o.frame.pendingCommit() {
		t.Fatalf("frame after EBUSY: %v, %d commits", err, len(*commits))
	}
}

// A cursor image change writes only an image the plane neither shows nor
// is about to show; with both busy it waits for the event.
func TestCursorImageNeverOverwritesScreen(t *testing.T) {
	o, _, _ := testOutput(t)
	c := o.cursor
	r := portsmocks.NewMockRenderer(t)
	var written []int
	r.EXPECT().WriteCursor(mock.Anything, mock.Anything, 1, 1).RunAndReturn(func(i int, _ []byte, _, _ int) error {
		written = append(written, i)
		return nil
	})
	px := []byte{1, 2, 3, 4}
	// Image 0 on screen: two changes before a commit both go to image 1.
	c.screen, c.cur, c.image = 90, 0, true
	for range 2 {
		if err := c.setImage(r, px, 1, 1, 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	if len(written) != 2 || written[0] != 1 || written[1] != 1 {
		t.Fatalf("written %v", written)
	}
	// Image 1 in flight, image 0 on screen: nothing is free.
	c.committed(cursorState{on: true, fb: 91})
	if err := c.setImage(r, px, 1, 1, 0, 0); err != nil || len(written) != 2 || c.later == nil {
		t.Fatalf("wrote a busy image: %v %v", err, written)
	}
	// The flip frees image 0.
	c.landed()
	if changed, err := c.flushLater(r); !changed || err != nil || len(written) != 3 || written[2] != 0 || c.cur != 0 {
		t.Fatalf("after flip: %v %v %v cur %d", changed, err, written, c.cur)
	}
}

// KMS refusing the cursor plane turns the hardware cursor off and the
// frame commits without it; the output keeps running.
func TestCursorRefusedFrameCommitsWithout(t *testing.T) {
	o, _, commits := testOutput(t, unix.EINVAL, unix.EINVAL)
	o.cursor.image = true
	o.cursor.Move(5, 5)
	if err := o.commitFrame(70, nil, false, false, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	if !o.cursor.off || len(*commits) != 3 || (*commits)[1].flags != atomicTestOnly {
		t.Fatalf("off=%v commits %d", o.cursor.off, len(*commits))
	}
	if v, _ := (*commits)[2].req.value(tCursor, pFB); v != 0 {
		t.Fatalf("retry shows cursor fb %d", v)
	}
}

// A refused cursor is not blamed on the client buffer.
func TestCursorRefusedDoesNotBlameScanoutBuffer(t *testing.T) {
	o, _, _ := testOutput(t, unix.EINVAL, unix.EINVAL)
	o.cursor.image = true
	o.cursor.Move(5, 5)
	o.clientFBs[9] = &clientFB{fbID: 80}
	direct, err := o.commitScanout(80, ports.SurfaceContent{DMABuf: &ports.DMABuf{ID: 9}}, pendingFrame{})
	if err != nil || !direct || o.clientFBs[9].failed != "" {
		t.Fatalf("direct=%v err=%v failed=%q", direct, err, o.clientFBs[9].failed)
	}
}

// showImages validates the images with a TEST_ONLY modeset, retries with
// single-plane driver images, then falls back to linear images when KMS
// refuses both.
func TestShowImagesTestsBeforeModeset(t *testing.T) {
	o, k, commits := testOutput(t, unix.EINVAL, unix.EINVAL)
	o.cursor = nil
	r := portsmocks.NewMockRenderer(t)
	buf := func() ports.DMABuf {
		var b ports.DMABuf
		for range 2 {
			f, w, _ := os.Pipe()
			w.Close()
			b.Planes = append(b.Planes, ports.DMABufPlane{File: f})
		}
		return b
	}
	linear := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, []uint64(nil), false).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().ExportTargets(2, []uint64(nil), true).Return([]ports.DMABuf{linear(), linear()}, nil).Once()
	r.EXPECT().ExportTargets(2, []uint64{0}, false).Return([]ports.DMABuf{linear(), linear()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil)
	k.EXPECT().rmFB(mock.Anything).Return(nil)
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil)
	if err := o.showImages(r, imagesDriver, nil); err != nil {
		t.Fatal(err)
	}
	want := []uint32{atomicTestOnly | atomicAllowModes, atomicTestOnly | atomicAllowModes, atomicTestOnly | atomicAllowModes, atomicAllowModes}
	if len(*commits) != len(want) {
		t.Fatalf("%d commits", len(*commits))
	}
	for i, f := range want {
		if (*commits)[i].flags != f {
			t.Fatalf("commit %d flags %#x want %#x", i, (*commits)[i].flags, f)
		}
	}
	if o.kind != imagesLinear {
		t.Fatalf("kind %d", o.kind)
	}
}

// A refused multi-plane (DCC) driver image is retried with single-plane
// driver images before linear; every plane file of every buffer is closed
// once the framebuffers exist.
func TestShowImagesRetriesSinglePlaneBeforeLinear(t *testing.T) {
	o, k, _ := testOutput(t, unix.EINVAL)
	o.cursor = nil
	var files []*os.File
	buf := func(planes int) ports.DMABuf {
		b := ports.DMABuf{}
		for range planes {
			f, w, _ := os.Pipe()
			w.Close()
			files = append(files, f)
			b.Planes = append(b.Planes, ports.DMABufPlane{File: f})
		}
		return b
	}
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, []uint64(nil), false).Return([]ports.DMABuf{buf(3), buf(3)}, nil).Once()
	r.EXPECT().ExportTargets(2, []uint64(nil), true).Return([]ports.DMABuf{buf(1), buf(1)}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
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
	if len(files) != 8 {
		t.Fatalf("%d plane files", len(files))
	}
	for i, f := range files {
		if err := f.Close(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("plane file %d left open (close: %v)", i, err)
		}
	}
}

// SDR images are asked for the modifiers the primary plane lists for
// XRGB8888, in IN_FORMATS order, never the renderer's own choice: RADV's
// preferred DCC variant (pipe-aligned) is not one amdgpu lists, and KMS
// refuses a framebuffer with a modifier the plane does not advertise
// (ADDFB2: EINVAL). Other formats' modifiers are not offered.
func TestShowImagesOffersThePrimaryPlanesXRGBModifiers(t *testing.T) {
	const dccListed, tiled, other = 0x200000000563b03, 0x200000000401b03, 0x200000000401903
	o, k, _ := testOutput(t)
	o.cursor = nil
	o.primary.formats = []ports.DMABufFormat{
		{Format: fourccXRGB, Modifier: dccListed}, {Format: fourccXR30, Modifier: other},
		{Format: fourccXRGB, Modifier: tiled}, {Format: fourccXRGB, Modifier: 0},
	}
	buf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, []uint64{dccListed, tiled, 0}, false).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil)
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil)
	if err := o.showImages(r, imagesDriver, nil); err != nil {
		t.Fatal(err)
	}
	if o.kind != imagesDriver {
		t.Fatalf("kind %d", o.kind)
	}
}

// When the renderer has no modifier in common with the plane, the images
// fall back to single-plane ones with the same list, then to linear.
func TestShowImagesNoCommonModifierFallsBackWithThePlanesList(t *testing.T) {
	const listed = 0x200000000563b03
	o, k, _ := testOutput(t)
	o.cursor = nil
	o.primary.formats = []ports.DMABufFormat{{Format: fourccXRGB, Modifier: listed}, {Format: fourccXRGB, Modifier: 0}}
	buf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	none := errors.New("no XRGB8888 modifier both the device and the display accept")
	r.EXPECT().ExportTargets(2, []uint64{listed, 0}, false).Return(nil, none).Once()
	r.EXPECT().ExportTargets(2, []uint64{listed, 0}, true).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil)
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil)
	if err := o.showImages(r, imagesDriver, nil); err != nil {
		t.Fatal(err)
	}
	if o.kind != imagesSinglePlane {
		t.Fatalf("kind %d", o.kind)
	}
}

// Refused driver images that already had one plane are not retried as
// single-plane images: the next export is linear.
func TestShowImagesSkipsSinglePlaneWhenDriverImagesHadOnePlane(t *testing.T) {
	o, k, commits := testOutput(t, unix.EINVAL)
	o.cursor = nil
	buf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, []uint64(nil), false).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().ExportTargets(2, []uint64{0}, false).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil)
	k.EXPECT().rmFB(mock.Anything).Return(nil)
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil)
	if err := o.showImages(r, imagesDriver, nil); err != nil {
		t.Fatal(err)
	}
	if o.kind != imagesLinear || len(*commits) != 3 {
		t.Fatalf("kind %d, %d commits", o.kind, len(*commits))
	}
}

// Enabling an output whose images were not made yet (started switched
// away) tests them and modesets once.
func TestEnableAfterStartAwayModesetsOnce(t *testing.T) {
	o, k, commits, commitMu := testOutputMu(t)
	o.cursor, o.tearing, o.fbs = nil, false, [2]uint32{}
	r := portsmocks.NewMockRenderer(t)
	buf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, mock.Anything, false).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Twice()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil)
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	active := make(chan bool, 1)
	active <- false
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, active, nil, nil, nil, make(chan ports.OutputPresented, 1), nil, nil)
	}()
	count := func(flags uint32) int {
		commitMu.Lock()
		defer commitMu.Unlock()
		n := 0
		for _, c := range *commits {
			if c.flags == flags {
				n++
			}
		}
		return n
	}
	time.Sleep(20 * time.Millisecond)
	if n := count(atomicAllowModes); n != 0 {
		t.Fatalf("%d modesets while away", n)
	}
	active <- true
	waitFor(t, func() bool { return count(atomicAllowModes) == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := count(atomicTestOnly | atomicAllowModes); n != 1 {
		t.Fatalf("%d test modesets", n)
	}
}

// Close without a mode to restore turns the CRTC off.
func TestCloseWithoutSavedModeDisablesCrtc(t *testing.T) {
	o, k, commits := testOutput(t)
	k.EXPECT().rmFB(mock.Anything).Return(nil)
	o.Close()
	c := (*commits)[0]
	for _, p := range []struct{ obj, prop uint32 }{{tCrtc, pActive}, {tCrtc, pMode}, {tPrimary, pFB}, {tConn, pConnCrtc}} {
		if v, ok := c.req.value(p.obj, p.prop); !ok || v != 0 {
			t.Fatalf("object %d prop %d = %d %v", p.obj, p.prop, v, ok)
		}
	}
}

// Close restores the saved mode and gives the connector its CRTC back.
func TestCloseRestoresSavedMode(t *testing.T) {
	o, k, commits := testOutput(t)
	o.saved = modeCrtc{crtcID: tCrtc, fbID: 5, modeValid: 1, mode: modeInfo{HDisplay: 200, VDisplay: 100}}
	k.EXPECT().createBlob(mock.Anything).Return(98, nil).Once()
	k.EXPECT().destroyBlob(uint32(98)).Return(nil).Once()
	k.EXPECT().rmFB(mock.Anything).Return(nil)
	o.Close()
	c := (*commits)[0]
	if v, _ := c.req.value(tConn, pConnCrtc); v != tCrtc {
		t.Fatalf("connector crtc %d", v)
	}
	if v, _ := c.req.value(tPrimary, pFB); v != 5 {
		t.Fatalf("primary fb %d", v)
	}
}

// With the cursor off no image is loaded (no warning per change).
func TestCursorOffSkipsLoads(t *testing.T) {
	o, _, _ := testOutput(t)
	o.cursor.off = true
	loaded := false
	o.setCursor(nil, func(ports.CursorChange, float64, ports.BufferTransform, int) (ports.CursorImage, error) {
		loaded = true
		return ports.CursorImage{}, nil
	}, ports.CursorChange{}, 1, 0)
	if loaded || o.cursor.desired().on {
		t.Fatal("cursor used while off")
	}
}

// Content read while only a cursor commit is in flight is reported when
// that commit's event arrives, not held until a later frame.
func TestRunReportsSeenAfterCursorCommit(t *testing.T) {
	o, k, commits, commitMu := testOutputMu(t)
	o.tearing, o.cursor.fbs = false, [2]uint32{}
	flips := make(chan flipEvent, 1)
	o.flipped = flips
	r := portsmocks.NewMockRenderer(t)
	buf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().CursorBuffers(64).Return([2]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().WriteCursor(mock.Anything, mock.Anything, 1, 1).Return(nil)
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, mock.Anything, false).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil)
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccARGB)).Return(90, nil).Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccARGB)).Return(91, nil).Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Twice()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	load := func(ports.CursorChange, float64, ports.BufferTransform, int) (ports.CursorImage, error) {
		return ports.CursorImage{Pixels: []byte{1, 2, 3, 4}, W: 1, H: 1}, nil
	}
	last := func(kind uint64) (commitRec, bool) {
		commitMu.Lock()
		defer commitMu.Unlock()
		for i := len(*commits) - 1; i >= 0; i-- {
			if c := (*commits)[i]; c.user&3 == kind {
				return c, true
			}
		}
		return commitRec{}, false
	}
	scenes := make(chan ports.Scene, 1)
	contents := make(chan ports.SurfaceContent, 1)
	presented := make(chan ports.OutputPresented, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, load, make(chan bool), scenes, contents, nil, presented, nil, nil)
	}()
	scenes <- ports.Scene{OutputWidth: 200, OutputHeight: 100, Scale: 1}
	var frame commitRec
	waitFor(t, func() bool { var ok bool; frame, ok = last(userFrame); return ok })
	flips <- eventOf(frame)
	<-presented // the frame's flip
	o.cursor.Move(3, 3)
	var cur commitRec
	waitFor(t, func() bool { var ok bool; cur, ok = last(userState); return ok })
	// A window this output does not show commits while the cursor flies.
	contents <- ports.SurfaceContent{ID: 7, Seq: 2, SHM: &ports.SHMBuffer{}}
	time.Sleep(20 * time.Millisecond)
	flips <- eventOf(cur)
	select {
	case p := <-presented:
		if p.Seen[7] != 2 {
			t.Fatalf("report %+v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("Seen not reported after the cursor commit")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Frame fences are closed on every path: flipped, EBUSY (re-rendered
// after the event) and lost master (re-rendered after the modeset). The
// fd count is unchanged after many frames.
func TestRunClosesFrameFences(t *testing.T) {
	fds := func() int {
		ents, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Skip(err)
		}
		return len(ents)
	}
	const rounds = 30
	// Commit results: modeset test and modeset, then per round
	// EBUSY + flip, or lost master + modeset + flip, or a plain flip.
	errs := []error{nil, nil}
	for i := range rounds {
		switch i % 3 {
		case 0:
			errs = append(errs, unix.EBUSY, nil)
		case 1:
			errs = append(errs, unix.EACCES, nil, nil)
		default:
			errs = append(errs, nil)
		}
	}
	o, k, commits, commitMu := testOutputMu(t, errs...)
	o.cursor, o.tearing, o.fbs = nil, false, [2]uint32{}
	flips := make(chan flipEvent, 1)
	o.flipped = flips
	r := portsmocks.NewMockRenderer(t)
	buf := func() ports.DMABuf {
		f, w, _ := os.Pipe()
		w.Close()
		return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
	}
	r.EXPECT().SetHDR(float64(0)).Return().Maybe()
	r.EXPECT().ExportTargets(2, mock.Anything, false).Return([]ports.DMABuf{buf(), buf()}, nil).Once()
	r.EXPECT().UseTarget(mock.Anything).Return()
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(ports.Scene, map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		f, w, _ := os.Pipe()
		_, _ = w.Write([]byte{1})
		w.Close()
		return f, nil
	})
	r.EXPECT().Close().Return().Once()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(70, nil).Twice()
	k.EXPECT().createBlob(mock.Anything).Return(99, nil)
	k.EXPECT().destroyBlob(mock.Anything).Return(nil)
	k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
	before := fds()
	active := make(chan bool, 1)
	scenes := make(chan ports.Scene, 1)
	presented := make(chan ports.OutputPresented, 4*rounds)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- o.Run(ctx, func(int, int) (ports.Renderer, error) { return r, nil }, nil, active, scenes, nil, nil, presented, nil, nil)
	}()
	count := func() int {
		commitMu.Lock()
		defer commitMu.Unlock()
		return len(*commits)
	}
	last := func() commitRec {
		commitMu.Lock()
		defer commitMu.Unlock()
		return (*commits)[len(*commits)-1]
	}
	waitFor(t, func() bool { return count() == 2 })
	for i := range rounds {
		n := count()
		scenes <- ports.Scene{Seq: uint64(i)}
		switch i % 3 {
		case 0:
			waitFor(t, func() bool { return count() == n+1 })
			flips <- flipEvent{crtc: tCrtc} // ends the EBUSY wait
			waitFor(t, func() bool { return count() == n+2 })
		case 1:
			waitFor(t, func() bool { return count() == n+1 })
			active <- true
			waitFor(t, func() bool { return count() == n+3 })
		default:
			waitFor(t, func() bool { return count() == n+1 })
		}
		flips <- eventOf(last())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if after := fds(); after > before {
		t.Fatalf("fds %d -> %d", before, after)
	}
}
