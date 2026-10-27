package drm

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/bnema/neferwl/internal/domain"
	"golang.org/x/sys/unix"
)

// ListGPUs discovers DRM cards through sysfs without opening device nodes.
// Optional attributes that cannot be read are left at their zero value.
func ListGPUs(sysRoot, devRoot string) ([]domain.GPUInfo, error) {
	class := filepath.Join(sysRoot, "class", "drm")
	entries, err := os.ReadDir(class)
	if err != nil {
		return nil, fmt.Errorf("list DRM cards: %w", err)
	}
	type card struct {
		name   string
		number uint64
	}
	var cards []card
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "card") {
			continue
		}
		suffix := strings.TrimPrefix(name, "card")
		if suffix == "" || strings.ContainsAny(suffix, "-+") {
			continue
		}
		n, err := strconv.ParseUint(suffix, 10, 64)
		if err == nil {
			cards = append(cards, card{name, n})
		}
	}
	slices.SortFunc(cards, func(a, b card) int {
		if a.number < b.number {
			return -1
		}
		if a.number > b.number {
			return 1
		}
		return strings.Compare(a.name, b.name)
	})
	result := make([]domain.GPUInfo, 0, len(cards))
	for _, c := range cards {
		path := filepath.Join(class, c.name)
		device := filepath.Join(path, "device")
		info := domain.GPUInfo{Path: filepath.Join(devRoot, "dri", c.name), Primary: readGPUDevice(filepath.Join(path, "dev"))}
		renders, _ := filepath.Glob(filepath.Join(device, "drm", "renderD*", "dev"))
		for _, render := range renders {
			if info.Render = readGPUDevice(render); info.Render != 0 {
				break
			}
		}
		if gpuLinkBase(filepath.Join(device, "subsystem")) == "pci" {
			info.PCI = gpuLinkBase(device)
		}
		info.Driver = gpuLinkBase(filepath.Join(device, "driver"))
		info.BootVGA = readGPUAttribute(filepath.Join(device, "boot_vga")) == "1"
		for _, entry := range entries {
			for _, kind := range []string{"eDP", "LVDS", "DSI"} {
				if strings.HasPrefix(entry.Name(), c.name+"-"+kind+"-") && readGPUAttribute(filepath.Join(class, entry.Name(), "status")) == "connected" {
					info.Internal = true
				}
			}
		}
		result = append(result, info)
	}
	return result, nil
}

func readGPUAttribute(path string) string {
	data, _ := os.ReadFile(path)
	return strings.TrimSpace(string(data))
}
func gpuLinkBase(path string) string {
	target, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}
func readGPUDevice(path string) uint64 {
	major, minor, ok := strings.Cut(readGPUAttribute(path), ":")
	if !ok {
		return 0
	}
	maj, err := strconv.ParseUint(major, 10, 32)
	if err != nil {
		return 0
	}
	min, err := strconv.ParseUint(minor, 10, 32)
	if err != nil {
		return 0
	}
	return unix.Mkdev(uint32(maj), uint32(min))
}
