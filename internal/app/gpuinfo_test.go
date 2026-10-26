package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestOpenCardRenderDeviceFallback(t *testing.T) {
	first := ports.GPUInfo{Path: "/dev/dri/card0", Render: unix.Mkdev(226, 128)}
	second := ports.GPUInfo{Path: "/dev/dri/card1", Render: unix.Mkdev(226, 129)}
	boom := errors.New("unsupported Vulkan")
	t.Run("fallback", func(t *testing.T) {
		opener := portsmocks.NewMockRenderDeviceOpener(t)
		dev := portsmocks.NewMockRenderDevice(t)
		opener.EXPECT().Open(first).Return(nil, boom).Once()
		opener.EXPECT().Open(second).Return(dev, nil).Once()
		got, err := openCardRenderDevice(opener, []ports.GPUInfo{first, second}, "")
		if err != nil || got != dev {
			t.Fatalf("%v %v", got, err)
		}
	})
	t.Run("all fail", func(t *testing.T) {
		opener := portsmocks.NewMockRenderDeviceOpener(t)
		opener.EXPECT().Open(first).Return(nil, boom).Once()
		opener.EXPECT().Open(second).Return(nil, boom).Once()
		_, err := openCardRenderDevice(opener, []ports.GPUInfo{first, second}, "")
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), first.Path) || !strings.Contains(err.Error(), second.Path) {
			t.Fatal(err)
		}
	})
	// /dev/null is a real character node, so override resolution uses real stat
	// without requiring access to a DRM node or a handwritten stat seam.
	var st unix.Stat_t
	if err := unix.Stat("/dev/null", &st); err != nil {
		t.Fatal(err)
	}
	t.Run("override unmatched", func(t *testing.T) {
		opener := portsmocks.NewMockRenderDeviceOpener(t)
		_, err := openCardRenderDevice(opener, []ports.GPUInfo{first, second}, "/dev/null")
		if err == nil {
			t.Fatal("unmatched override accepted")
		}
	})
	t.Run("override strict", func(t *testing.T) {
		opener := portsmocks.NewMockRenderDeviceOpener(t)
		selected := ports.GPUInfo{Path: "selected", Render: st.Rdev}
		opener.EXPECT().Open(selected).Return(nil, boom).Once()
		_, err := openCardRenderDevice(opener, []ports.GPUInfo{selected, second}, "/dev/null")
		if !errors.Is(err, boom) {
			t.Fatal(err)
		}
	})
	t.Run("empty", func(t *testing.T) {
		_, err := openCardRenderDevice(portsmocks.NewMockRenderDeviceOpener(t), nil, "")
		if err == nil || strings.Contains(err.Error(), `for ""`) {
			t.Fatal(err)
		}
	})
}
