package app

import (
	"errors"
	"github.com/bnema/neferwl/internal/core"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
)

func TestReopenRenderRecovery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		seconds   []int
		exhausted bool
	}{
		{"four rapid losses", []int{0, 1, 2, 3}, true},
		{"three spaced losses", []int{0, 61, 122}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				clock := portsmocks.NewMockClock(t)
				opener := portsmocks.NewMockRenderDeviceOpener(t)
				info := domain.GPUInfo{Path: "card1"}
				limiter := core.ReopenLimiter{Clock: clock}
				current := portsmocks.NewMockRenderDevice(t)
				var device ports.RenderDevice = current
				for i, seconds := range tc.seconds {
					r := portsmocks.NewMockRenderer(t)
					current.EXPECT().NewRenderer(8, 8, domain.RendererOptions{}).Return(r, nil).Once()
					r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, ports.ErrDeviceLost).Once()
					r.EXPECT().Close().Return().Once()
					renderer, err := device.NewRenderer(8, 8, domain.RendererOptions{})
					if err != nil {
						t.Fatal(err)
					}
					_, err = renderer.Render(domain.Scene{}, nil)
					if !errors.Is(err, ports.ErrDeviceLost) {
						t.Fatal(err)
					}
					renderer.Close()
					closed := false
					current.EXPECT().Close().Run(func() { closed = true }).Once()
					clock.EXPECT().Now().Return(time.Unix(int64(seconds), 0)).Once()
					var next *portsmocks.MockRenderDevice
					if !tc.exhausted || i < 3 {
						next = portsmocks.NewMockRenderDevice(t)
						opener.EXPECT().Open(info).Run(func(domain.GPUInfo) {
							if !closed {
								t.Error("open before close")
							}
						}).Return(next, nil).Once()
					}
					device, err = reopenRender(opener, info, device, &limiter)
					if tc.exhausted && i == 3 {
						if !errors.Is(err, ports.ErrDeviceLost) {
							t.Fatal(err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					current = next
				}
				current.EXPECT().Close().Return().Once()
				device.Close()
			})
		})
	}
}

func TestReopenRenderFailedAttempts(t *testing.T) {
	clock := portsmocks.NewMockClock(t)
	clock.EXPECT().Now().Return(time.Unix(0, 0)).Times(4)
	opener := portsmocks.NewMockRenderDeviceOpener(t)
	info := domain.GPUInfo{Path: "card1"}
	openErr := errors.New("open failed")
	opener.EXPECT().Open(info).Return(nil, openErr).Times(3)
	old := portsmocks.NewMockRenderDevice(t)
	old.EXPECT().Close().Return().Once()
	dev, err := reopenRender(opener, info, old, &core.ReopenLimiter{Clock: clock})
	if dev != nil || !errors.Is(err, ports.ErrDeviceLost) || !errors.Is(err, openErr) {
		t.Fatalf("%v %v", dev, err)
	}
}
