package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/bnema/zerowrap"
	"testing"
	"testing/synctest"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

// The real seat/KMS boundary is not injected: exercise selection/open and
// outputSet startup with the same device, rather than opening physical cards.
func TestOneRenderDeviceTwoOutputs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		opener := portsmocks.NewMockRenderDeviceOpener(t)
		dev := portsmocks.NewMockRenderDevice(t)
		info := ports.GPUInfo{Path: "/dev/dri/card1", Primary: unix.Mkdev(226, 1), Render: unix.Mkdev(226, 128)}
		opener.EXPECT().Open(info).Return(dev, nil).Once()
		device, _, err := openPlannedRender(opener, []cardPlan{{Info: info, Render: true}}, false, zerowrap.Logger{})
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			r := portsmocks.NewMockRenderer(t)
			r.EXPECT().Close().Return().Once()
			dev.EXPECT().NewRenderer(64, 32, ports.RendererOptions{}).Return(r, nil).Once()
		}
		set := newOutputSet(context.Background(), nil, outputRoutes{})
		run := func(ctx context.Context, _ outputFeed) error {
			r, err := device.NewRenderer(64, 32, ports.RendererOptions{})
			if err != nil {
				return err
			}
			defer r.Close()
			<-ctx.Done()
			return nil
		}
		set.start(t.Context(), "A", run)
		set.start(t.Context(), "B", run)
		synctest.Wait()
		if err := set.wait(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestOpenPlannedRenderFallback(t *testing.T) {
	first := ports.GPUInfo{Path: "card0", Render: 1}
	second := ports.GPUInfo{Path: "card1", Render: 2}
	plans := []cardPlan{{Info: first, Render: true}, {Info: second}}
	boom := errors.New("unsupported Vulkan")
	for _, strict := range []bool{false, true} {
		t.Run(fmt.Sprint(strict), func(t *testing.T) {
			opener := portsmocks.NewMockRenderDeviceOpener(t)
			opener.EXPECT().Open(first).Return(nil, boom).Once()
			var want ports.RenderDevice
			if !strict {
				dev := portsmocks.NewMockRenderDevice(t)
				want = dev
				opener.EXPECT().Open(second).Return(dev, nil).Once()
			}
			got, info, err := openPlannedRender(opener, plans, strict, zerowrap.Logger{})
			if strict {
				if !errors.Is(err, boom) {
					t.Fatal(err)
				}
			} else if err != nil || got != want || info != second {
				t.Fatalf("%v %+v %v", got, info, err)
			}
		})
	}
}
