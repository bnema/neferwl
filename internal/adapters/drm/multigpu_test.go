package drm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"testing/synctest"

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
	o.primary.formats = []ports.DMABufFormat{{Format: fourccXRGB, Modifier: 0}, {Format: fourccXRGB, Modifier: tiledMod}}
	r := gbmRenderer(t)
	r.EXPECT().TargetModifiers([]uint64{0}, true).Return([]uint64{0}).Once()
	r.EXPECT().ImportTargets(mock.Anything).Return(nil).Once()
	allocator.EXPECT().create(200, 100, uint32(fourccXRGB), []uint64{0}).RunAndReturn(func(int, int, uint32, []uint64) (imageBuffer, error) {
		img := newMockimageBuffer(t)
		img.EXPECT().dmabuf().RunAndReturn(func() (ports.DMABuf, error) { b := gbmBuf(1); b.Modifier = 0; return b, nil }).Once()
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
			opener.EXPECT().Open(ports.GPUInfo{}).Return(local, nil).Once()
			local.EXPECT().NewCopier(200, 100).Return(copier, nil).Once()
			formats := []ports.DMABufFormat{{Format: fourccXRGB, Modifier: 0}}
			local.EXPECT().DMABuf().Return(ports.DMABufSupport{Formats: formats}).Once()
			copier.EXPECT().SourceModifiers(formats).Return(formats).Once()
			r := gbmRenderer(t)
			sources := []ports.DMABuf{gbmBuf(1), gbmBuf(1)}
			r.EXPECT().ExportTargets(2, []uint64{0}, false).Return(sources, nil).Once()
			copier.EXPECT().ImportSources(sources).Return(nil).Once()
			closed := false
			copier.EXPECT().Close().Run(func() { closed = true }).Once()
			alloc.EXPECT().create(200, 100, uint32(fourccXRGB), []uint64(nil)).RunAndReturn(func(int, int, uint32, []uint64) (imageBuffer, error) {
				img := newMockimageBuffer(t)
				img.EXPECT().dmabuf().RunAndReturn(func() (ports.DMABuf, error) { return gbmBuf(1), nil }).Once()
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

func TestMultiGPUCopyCreationError(t *testing.T) {
	o, _, _ := testOutput(t)
	o.fbs = [2]uint32{}
	local := portsmocks.NewMockRenderDevice(t)
	opener := portsmocks.NewMockRenderDeviceOpener(t)
	o.card = &Card{dev: CardDevices{Local: func() (ports.RenderDevice, error) { return opener.Open(ports.GPUInfo{}) }}}
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
			if fail {
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
