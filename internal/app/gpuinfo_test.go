package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

func TestCardGPUInfo(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "dev/char/226:1/device/drm/renderD128/dev")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("226:128\n"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := cardGPUInfo(root, "/dev/dri/card1", unix.Mkdev(226, 1))
	if err != nil || info.Render != unix.Mkdev(226, 128) || info.Primary != unix.Mkdev(226, 1) {
		t.Fatalf("%+v %v", info, err)
	}
	info, err = cardGPUInfo(root, "/dev/dri/card2", unix.Mkdev(226, 2))
	if err != nil || info.Render != 0 {
		t.Fatalf("%+v %v", info, err)
	}
}

// The real seat/KMS boundary is not injected: exercise selection/open and
// outputSet startup with the same device, rather than opening physical cards.
func TestOneRenderDeviceTwoOutputs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		opener := portsmocks.NewMockRenderDeviceOpener(t)
		dev := portsmocks.NewMockRenderDevice(t)
		info := ports.GPUInfo{Path: "/dev/dri/card1", Primary: unix.Mkdev(226, 1), Render: unix.Mkdev(226, 128)}
		opener.EXPECT().Open(info).Return(dev, nil).Once()
		device, err := openCardRenderDevice(opener, []ports.GPUInfo{{Path: "/dev/dri/card0"}, info}, "")
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
