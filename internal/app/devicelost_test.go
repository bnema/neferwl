package app

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
)

func TestReopenLimiter(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seconds []int
		want    []bool
	}{
		{"three then refused", []int{0, 10, 20, 30}, []bool{true, true, true, false}},
		{"expires after 61 seconds", []int{0, 0, 0, 0, 61}, []bool{true, true, true, false, true}},
		{"inclusive 60 second boundary", []int{0, 0, 0, 60, 61}, []bool{true, true, true, false, true}},
		{"only expired entries dropped", []int{0, 30, 40, 61, 62, 91}, []bool{true, true, true, true, false, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := portsmocks.NewMockClock(t)
			base := time.Unix(1000, 0)
			for _, seconds := range tc.seconds {
				clock.EXPECT().Now().Return(base.Add(time.Duration(seconds) * time.Second)).Once()
			}
			l := reopenLimiter{clock: clock}
			for i, want := range tc.want {
				if got := l.allow(); got != want {
					t.Fatalf("attempt %d at %ds: got %v want %v", i, tc.seconds[i], got, want)
				}
			}
		})
	}
}

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
				info := ports.GPUInfo{Path: "card1"}
				limiter := reopenLimiter{clock: clock}
				current := portsmocks.NewMockRenderDevice(t)
				var device ports.RenderDevice = current
				for i, seconds := range tc.seconds {
					r := portsmocks.NewMockRenderer(t)
					current.EXPECT().NewRenderer(8, 8, ports.RendererOptions{}).Return(r, nil).Once()
					r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, ports.ErrDeviceLost).Once()
					r.EXPECT().Close().Return().Once()
					renderer, err := device.NewRenderer(8, 8, ports.RendererOptions{})
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
						opener.EXPECT().Open(info).Run(func(ports.GPUInfo) {
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
	info := ports.GPUInfo{Path: "card1"}
	openErr := errors.New("open failed")
	opener.EXPECT().Open(info).Return(nil, openErr).Times(3)
	old := portsmocks.NewMockRenderDevice(t)
	old.EXPECT().Close().Return().Once()
	dev, err := reopenRender(opener, info, old, &reopenLimiter{clock: clock})
	if dev != nil || !errors.Is(err, ports.ErrDeviceLost) || !errors.Is(err, openErr) {
		t.Fatalf("%v %v", dev, err)
	}
}
