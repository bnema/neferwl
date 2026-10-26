package drm

import (
	"errors"
	"testing"

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
	for _, mode := range []MultiGPU{MultiGPUDirect, MultiGPUAuto} {
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
