package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

// cardGPUInfo resolves the render node associated with an already opened card.
func cardGPUInfo(root, path string, primary uint64) (ports.GPUInfo, error) {
	info := ports.GPUInfo{Path: path, Primary: primary}
	files, err := filepath.Glob(filepath.Join(root, "dev", "char", fmt.Sprintf("%d:%d", unix.Major(primary), unix.Minor(primary)), "device", "drm", "renderD*", "dev"))
	if err != nil {
		return info, err
	}
	if len(files) == 0 {
		return info, nil
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		return info, err
	}
	var major, minor uint32
	if _, err = fmt.Sscanf(strings.TrimSpace(string(data)), "%d:%d", &major, &minor); err != nil {
		return info, fmt.Errorf("render node for %s: %w", path, err)
	}
	info.Render = unix.Mkdev(major, minor)
	return info, nil
}

func openCardRenderDevice(opener ports.RenderDeviceOpener, infos []ports.GPUInfo, override string) (ports.RenderDevice, error) {
	var want uint64
	if override != "" {
		var st unix.Stat_t
		if err := unix.Stat(override, &st); err != nil {
			return nil, fmt.Errorf("render device %q: %w", override, err)
		}
		want = st.Rdev
	}
	var errs []error
	for _, info := range infos {
		if info.Render == 0 || (override != "" && info.Render != want) {
			continue
		}
		dev, err := opener.Open(info)
		if err == nil {
			return dev, nil
		}
		err = fmt.Errorf("render card %s: %w", info.Path, err)
		if override != "" {
			return nil, err
		}
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if override != "" {
		return nil, fmt.Errorf("no render-capable DRM card for %q", override)
	}
	return nil, errors.New("no render-capable DRM card")
}
