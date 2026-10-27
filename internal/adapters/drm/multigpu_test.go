package drm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

func TestMultiGPUOffScan(t *testing.T) {
	k := newMockkms(t)
	dev := portsmocks.NewMockRenderDevice(t)
	opener := portsmocks.NewMockRenderDeviceOpener(t)
	c := &Card{k: k, info: ports.GPUInfo{Path: "card1"}, dev: CardDevices{Render: dev, Local: func() (ports.RenderDevice, error) { return opener.Open(ports.GPUInfo{}) }, Mode: MultiGPUOff}}
	k.EXPECT().resources().Return([]uint32{30}, []uint32{40}, nil).Once()
	k.EXPECT().connector(uint32(40)).Return(connector{id: 40, name: "DP-1", connected: true}, nil).Once()
	added, _, _, err := c.Scan()
	if err != nil || len(added) != 0 {
		t.Fatalf("%v %v", added, err)
	}
}

func TestMultiGPUDirectImages(t *testing.T) {
	o, k, _ := testOutput(t)
	o.cursor = nil
	allocator := newMockimageAllocator(t)
	opener := portsmocks.NewMockRenderDeviceOpener(t)
	o.card = &Card{alloc: allocator, dev: CardDevices{Mode: MultiGPUDirect, Local: func() (ports.RenderDevice, error) { return opener.Open(ports.GPUInfo{}) }}}
	o.primary.formats = []domain.DMABufFormat{{Format: fourccXRGB, Modifier: 0}, {Format: fourccXRGB, Modifier: tiledMod}}
	r := gbmRenderer(t)
	r.EXPECT().TargetModifiers([]uint64{0}, true).Return([]uint64{0}).Once()
	r.EXPECT().ImportTargets(mock.Anything).Return(nil).Once()
	allocator.EXPECT().create(200, 100, uint32(fourccXRGB), []uint64{0}).RunAndReturn(func(int, int, uint32, []uint64) (imageBuffer, error) {
		img := newMockimageBuffer(t)
		img.EXPECT().dmabuf().RunAndReturn(func() (domain.DMABuf, error) { b := gbmBuf(1); b.Modifier = 0; return b, nil }).Once()
		img.EXPECT().close().Return().Once()
		return img, nil
	}).Twice()
	k.EXPECT().rmFB(mock.Anything).Return(nil).Times(4)
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(80, nil).Twice()
	kind, err := o.setupImages(r, imagesDriver, nil, false)
	if err != nil || kind != imagesLinear || o.transport != transportDirect {
		t.Fatalf("%v %v %v", kind, o.transport, err)
	}
	o.freeImages()
}

func TestMultiGPUDirectRefusesFallback(t *testing.T) {
	for _, mode := range []MultiGPU{MultiGPUDirect} {
		o, _, _ := testOutput(t)
		opener := portsmocks.NewMockRenderDeviceOpener(t)
		o.card = &Card{dev: CardDevices{Mode: mode, Local: func() (ports.RenderDevice, error) { return opener.Open(ports.GPUInfo{}) }}}
		r := portsmocks.NewMockRenderer(t)
		if _, err := o.setupImages(r, imagesDriver, nil, false); err == nil {
			t.Fatal("failure accepted")
		}
		if _, err := o.setupImages(r, imagesDriver, errors.New("modeset refused"), true); err == nil {
			t.Fatal("skipGBM bypassed ladder")
		}
	}
}

func TestLocalDeviceErrorDoesNotRecoverRenderGPU(t *testing.T) {
	err := localDeviceError{cause: ports.ErrDeviceLost}
	if errors.Is(err, ports.ErrDeviceLost) {
		t.Fatal("local error exposes render-device sentinel")
	}
}

func TestMultiGPUCopySetup(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			o, k, _ := testOutput(t)
			o.cursor = nil
			o.fbs = [2]uint32{}
			local := portsmocks.NewMockRenderDevice(t)
			opener := portsmocks.NewMockRenderDeviceOpener(t)
			copier := portsmocks.NewMockCopier(t)
			alloc := newMockimageAllocator(t)
			o.card = &Card{alloc: alloc, dev: CardDevices{Mode: MultiGPUAuto, Local: func() (ports.RenderDevice, error) { return opener.Open(ports.GPUInfo{}) }}}
			// The display card scans out tiled only: GBM must receive these.
			o.primary.formats = []domain.DMABufFormat{{Format: fourccXRGB, Modifier: tiledMod}}
			opener.EXPECT().Open(ports.GPUInfo{}).Return(local, nil).Once()
			local.EXPECT().NewCopier(200, 100).Return(copier, nil).Once()
			formats := []domain.DMABufFormat{{Format: fourccXRGB, Modifier: 0}}
			local.EXPECT().DMABuf().Return(domain.DMABufSupport{Formats: formats}).Once()
			copier.EXPECT().SourceModifiers(formats).Return(formats).Once()
			r := gbmRenderer(t)
			sources := []domain.DMABuf{gbmBuf(1), gbmBuf(1)}
			r.EXPECT().ExportTargets(2, []uint64{0}, false).Return(sources, nil).Once()
			copier.EXPECT().ImportSources(sources).Return(nil).Once()
			closed := false
			copier.EXPECT().Close().Run(func() { closed = true }).Once()
			alloc.EXPECT().create(200, 100, uint32(fourccXRGB), []uint64{tiledMod}).RunAndReturn(func(int, int, uint32, []uint64) (imageBuffer, error) {
				img := newMockimageBuffer(t)
				img.EXPECT().dmabuf().RunAndReturn(func() (domain.DMABuf, error) { return gbmBuf(1), nil }).Once()
				img.EXPECT().close().Run(func() {
					if !closed {
						t.Error("image freed before copier")
					}
				}).Once()
				return img, nil
			}).Twice()
			var importErr error
			if fail {
				importErr = ports.ErrDeviceLost
				r.EXPECT().ExportTargets(0, []uint64(nil), false).Return(nil, nil).Once()
			} else {
				copier.EXPECT().Copy(0, 0, (*os.File)(nil)).Return(nil, nil).Once()
				copier.EXPECT().Copy(1, 1, (*os.File)(nil)).Return(nil, nil).Once()
				k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(80, nil).Twice()
				k.EXPECT().rmFB(uint32(80)).Return(nil).Twice()
			}
			copier.EXPECT().ImportTargets(mock.Anything).Return(importErr).Once()
			_, err := o.setupImages(r, imagesDriver, nil, false)
			if fail {
				if err == nil || errors.Is(err, ports.ErrDeviceLost) {
					t.Fatalf("local failure: %v", err)
				}
			} else {
				if err != nil || o.transport != transportCopy {
					t.Fatalf("%v %v", o.transport, err)
				}
				o.freeImages()
			}
		})
	}
}

// copyFixture is an output on a cross-GPU card whose local device offers
// a copier that accepts the given source modifiers.
func copyFixture(t *testing.T, mods []domain.DMABufFormat) (*Output, *mockkms, *portsmocks.MockRenderer, *portsmocks.MockCopier, *mockimageAllocator) {
	o, k, _ := testOutput(t)
	o.cursor = nil
	o.fbs = [2]uint32{}
	local := portsmocks.NewMockRenderDevice(t)
	opener := portsmocks.NewMockRenderDeviceOpener(t)
	copier := portsmocks.NewMockCopier(t)
	alloc := newMockimageAllocator(t)
	o.card = &Card{alloc: alloc, dev: CardDevices{Mode: MultiGPUAuto, Local: func() (ports.RenderDevice, error) { return opener.Open(ports.GPUInfo{}) }}}
	o.primary.formats = []domain.DMABufFormat{{Format: fourccXRGB, Modifier: tiledMod}}
	opener.EXPECT().Open(ports.GPUInfo{}).Return(local, nil).Once()
	local.EXPECT().NewCopier(200, 100).Return(copier, nil).Once()
	local.EXPECT().DMABuf().Return(domain.DMABufSupport{Formats: mods}).Once()
	copier.EXPECT().SourceModifiers(mods).Return(mods).Once()
	return o, k, gbmRenderer(t), copier, alloc
}

func TestMultiGPUCopySourceModifiers(t *testing.T) {
	t.Run("no LINEAR", func(t *testing.T) {
		mods := []domain.DMABufFormat{{Format: fourccXRGB, Modifier: tiledMod}}
		o, _, r, copier, _ := copyFixture(t, mods)
		r.EXPECT().ExportTargets(2, []uint64{tiledMod}, false).Return([]domain.DMABuf{gbmBuf(1), gbmBuf(1)}, nil).Once()
		copier.EXPECT().ImportSources(mock.Anything).Return(ports.ErrDeviceLost).Once()
		copier.EXPECT().Close().Return().Once()
		r.EXPECT().ExportTargets(0, []uint64(nil), false).Return(nil, nil).Once()
		if _, err := o.setupImages(r, imagesDriver, nil, false); err == nil {
			t.Fatal("import failure accepted")
		}
	})
	t.Run("LINEAR only is not retried", func(t *testing.T) {
		mods := []domain.DMABufFormat{{Format: fourccXRGB, Modifier: 0}}
		o, _, r, copier, _ := copyFixture(t, mods)
		r.EXPECT().ExportTargets(2, []uint64{0}, false).Return(nil, errors.New("not exportable")).Once()
		copier.EXPECT().Close().Return().Once()
		r.EXPECT().ExportTargets(0, []uint64(nil), false).Return(nil, nil).Once()
		if _, err := o.setupImages(r, imagesDriver, nil, false); err == nil {
			t.Fatal("export failure accepted")
		}
	})
}

func TestMultiGPUCopyShowFailureRestores(t *testing.T) {
	mods := []domain.DMABufFormat{{Format: fourccXRGB, Modifier: 0}}
	o, k, r, copier, alloc := copyFixture(t, mods)
	o.transport = transportDirect
	r.EXPECT().ExportTargets(2, []uint64{0}, false).Return([]domain.DMABuf{gbmBuf(1), gbmBuf(1)}, nil).Once()
	copier.EXPECT().ImportSources(mock.Anything).Return(nil).Once()
	copier.EXPECT().ImportTargets(mock.Anything).Return(nil).Once()
	freed := 0
	alloc.EXPECT().create(200, 100, uint32(fourccXRGB), []uint64{tiledMod}).RunAndReturn(func(int, int, uint32, []uint64) (imageBuffer, error) {
		img := newMockimageBuffer(t)
		img.EXPECT().dmabuf().RunAndReturn(func() (domain.DMABuf, error) { return gbmBuf(1), nil }).Once()
		img.EXPECT().close().Run(func() { freed++ }).Once()
		return img, nil
	}).Twice()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(0, errors.New("addfb refused")).Once()
	closed := false
	copier.EXPECT().Close().Run(func() { closed = true }).Once()
	r.EXPECT().ExportTargets(0, []uint64(nil), false).Return(nil, nil).Once()
	if _, err := o.setupImages(r, imagesDriver, nil, false); err == nil {
		t.Fatal("showBufs failure accepted")
	}
	if o.transport != transportDirect || !closed || o.copier != nil || freed != 2 || len(o.images) != 0 {
		t.Fatalf("transport %q closed %v copier %v freed %d images %d", o.transport, closed, o.copier, freed, len(o.images))
	}
}

func TestMultiGPUCopyCreationError(t *testing.T) {
	o, _, _ := testOutput(t)
	o.fbs = [2]uint32{}
	local := portsmocks.NewMockRenderDevice(t)
	opener := portsmocks.NewMockRenderDeviceOpener(t)
	o.card = &Card{alloc: newMockimageAllocator(t), dev: CardDevices{Local: func() (ports.RenderDevice, error) { return opener.Open(ports.GPUInfo{}) }}}
	opener.EXPECT().Open(ports.GPUInfo{}).Return(local, nil).Once()
	local.EXPECT().NewCopier(200, 100).Return(nil, ports.ErrDeviceLost).Once()
	_, err := o.setupImages(portsmocks.NewMockRenderer(t), imagesDriver, nil, false)
	if err == nil || errors.Is(err, ports.ErrDeviceLost) {
		t.Fatal(err)
	}
}

func TestMultiGPUCopyFrameFence(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			o, _, commits := testOutput(t)
			o.cursor = nil
			o.tearing = false
			o.vrrProp = 0
			r := portsmocks.NewMockRenderer(t)
			copier := portsmocks.NewMockCopier(t)
			o.copier = copier
			o.transport = transportCopy
			renderFence, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			w.Close()
			defer renderFence.Close()
			copyFence, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			w.Close()
			defer copyFence.Close()
			r.EXPECT().UseTarget(0).Return().Once()
			r.EXPECT().Render(mock.Anything, mock.Anything).Return(renderFence, nil).Once()
			copyFD := uint64(copyFence.Fd())
			var copyErr error
			if fail {
				copyErr = ports.ErrDeviceLost
			}
			copier.EXPECT().Copy(0, 0, renderFence).Return(copyFence, copyErr).Once()
			_, err = o.submitFrame(context.Background(), r, ports.Scene{Seq: 1}, nil, nil, nil, nil)
			// The render fence is consumed by the copy either way.
			if _, serr := renderFence.Stat(); serr == nil {
				t.Fatal("render fence left open")
			}
			if fail {
				if len(o.readFences) != 1 {
					t.Fatalf("readFences %d, want a dup of the render fence", len(o.readFences))
				}
				if !signalled(o.readFences) {
					t.Fatal("held fence is not the (signalled) render fence")
				}
				var fatal renderError
				if err == nil || errors.Is(err, ports.ErrDeviceLost) || !errors.As(err, &fatal) || len(*commits) != 0 {
					t.Fatalf("%v commits %d", err, len(*commits))
				}
				o.dropRead()
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if got, ok := (*commits)[0].req.value(tPrimary, pFence); !ok || got != copyFD {
					t.Fatalf("commit fence %d want %d", got, copyFD)
				}
			}
		})
	}
}

// The copy of a frame whose commit failed may still read the render target:
// the failure path waits for it before the next frame can render.
//
//testpolicy:realtime pipe fd poll in settleCopy; a bounded real wait shows submitFrame still blocked
func TestMultiGPUCopyCommitFailureWaitsForCopy(t *testing.T) {
	copyFence, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer copyFence.Close()
	committed := make(chan struct{}, 1)
	o, _, _, _ := testOutputRules(t, func(*atomicReq, uint32) error {
		committed <- struct{}{}
		return errors.New("commit refused")
	})
	o.cursor = nil
	o.tearing = false
	o.vrrProp = 0
	r := portsmocks.NewMockRenderer(t)
	copier := portsmocks.NewMockCopier(t)
	o.copier = copier
	o.transport = transportCopy
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
	copier.EXPECT().Copy(0, 0, (*os.File)(nil)).Return(copyFence, nil).Once()
	result := make(chan error, 1)
	go func() {
		_, err := o.submitFrame(context.Background(), r, ports.Scene{Seq: 1}, nil, nil, nil, nil)
		result <- err
	}()
	<-committed
	// The copy fence is unsignalled: the failure path must still be waiting.
	select {
	case <-result:
		t.Fatal("commit failure returned before the copy finished")
	case <-time.After(100 * time.Millisecond):
	}
	w.Close()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("commit failure accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("submitFrame did not return after the copy finished")
	}
	if len(o.readFences) != 1 || !signalled(o.readFences) {
		t.Fatalf("held fences %d", len(o.readFences))
	}
	o.dropRead()
}

func TestMultiGPUCopyCPUFence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o, _, commits := testOutput(t)
		o.cursor = nil
		o.tearing = false
		o.vrrProp = 0
		o.cpuFences = true
		r := portsmocks.NewMockRenderer(t)
		copier := portsmocks.NewMockCopier(t)
		o.copier = copier
		o.transport = transportCopy
		// The render fence stays unsignalled. Only the copy fence signals, proving
		// the existing CPU gate consumes the replacement, without waiting on render.
		renderFence, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		defer renderFence.Close()
		copied, cw, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		cw.Close()
		defer copied.Close()
		r.EXPECT().UseTarget(0).Return().Once()
		r.EXPECT().Render(mock.Anything, mock.Anything).Return(renderFence, nil).Once()
		copier.EXPECT().Copy(0, 0, renderFence).Return(copied, nil).Once()
		_, err = o.submitFrame(context.Background(), r, ports.Scene{Seq: 1}, nil, nil, nil, nil)
		if err != nil || len(*commits) != 1 || o.flipStats.fenceTimeouts != 0 {
			t.Fatalf("%v commits %d timeouts %d", err, len(*commits), o.flipStats.fenceTimeouts)
		}
		if _, ok := (*commits)[0].req.value(tPrimary, pFence); ok {
			t.Fatal("CPU fence gate submitted IN_FENCE_FD")
		}
	})
}

func TestDirectFrameNeverCopies(t *testing.T) {
	for _, cross := range []bool{false, true} {
		t.Run(fmt.Sprint(cross), func(t *testing.T) {
			o, _, _ := testOutput(t)
			o.cursor = nil
			o.tearing = false
			o.vrrProp = 0
			o.transport = transportDirect
			opener := portsmocks.NewMockRenderDeviceOpener(t)
			copier := portsmocks.NewMockCopier(t)
			// A stale copier must not be used unless the transport is gpu-copy.
			o.copier = copier
			o.card = &Card{}
			if cross {
				o.card.dev.Local = func() (ports.RenderDevice, error) { return opener.Open(ports.GPUInfo{}) }
			}
			r := portsmocks.NewMockRenderer(t)
			r.EXPECT().UseTarget(0).Return().Once()
			r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
			if _, err := o.submitFrame(context.Background(), r, ports.Scene{Seq: 1}, nil, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			copier.AssertNotCalled(t, "Copy", mock.Anything, mock.Anything, mock.Anything)
			opener.AssertNotCalled(t, "Open", mock.Anything)
		})
	}
}

func TestIsLocalDeviceError(t *testing.T) {
	lost := localDeviceError{cause: ports.ErrDeviceLost}
	if !IsLocalDeviceError(fmt.Errorf("output: %w", lost)) {
		t.Fatal("wrapped local error not recognized")
	}
	if errors.Is(lost, ports.ErrDeviceLost) {
		t.Fatal("local loss must not trigger shared render recovery")
	}
	if IsLocalDeviceError(ports.ErrDeviceLost) || IsLocalDeviceError(nil) {
		t.Fatal("shared loss or nil classified as local")
	}
}
