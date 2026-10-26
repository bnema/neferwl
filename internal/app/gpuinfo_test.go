package app

import (
	"errors"
	"fmt"
	"github.com/bnema/zerowrap"
	"testing"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

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
