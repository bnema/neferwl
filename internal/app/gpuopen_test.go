package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/zerowrap"
)

func TestOpenHeadlessRenderDiscovery(t *testing.T) {
	first := domain.GPUInfo{Path: "card0", Render: 1, PCI: "0001:03:00.0"}
	second := domain.GPUInfo{Path: "card1", Render: 2}
	boom := errors.New("unavailable")
	for _, name := range []string{"list error", "empty", "no render", "open failure", "second success", "constrained empty", "constrained list", "constrained open"} {
		t.Run(name, func(t *testing.T) {
			opener := portsmocks.NewMockRenderDeviceOpener(t)
			dev := portsmocks.NewMockRenderDevice(t)
			cfg := domain.Config{}
			var c []domain.GPUInfo
			var listErr error
			fail := strings.HasPrefix(name, "constrained")
			switch name {
			case "list error":
				listErr = boom
			case "no render":
				c = []domain.GPUInfo{{Path: "card0"}}
			case "open failure":
				c = []domain.GPUInfo{first}
				opener.EXPECT().Open(first).Return(nil, boom).Once()
			case "second success":
				c = []domain.GPUInfo{first, second}
				opener.EXPECT().Open(first).Return(nil, boom).Once()
				opener.EXPECT().Open(second).Return(dev, nil).Once()
			case "constrained empty":
				cfg.Render.Device = "nope"
			case "constrained list":
				cfg.Render.Device = "nope"
				listErr = boom
			case "constrained open":
				cfg.Render.Device = first.PCI
				c = []domain.GPUInfo{first}
				opener.EXPECT().Open(first).Return(nil, boom).Once()
			}
			if !fail && name != "second success" {
				opener.EXPECT().Open(domain.GPUInfo{}).Return(dev, nil).Once()
			}
			got, err := openHeadlessRender(opener, c, listErr, cfg, "", zerowrap.Logger{})
			if fail {
				if err == nil {
					t.Fatal("constraint discarded")
				}
			} else if err != nil || got != dev {
				t.Fatalf("%v %v", got, err)
			}
		})
	}
}
