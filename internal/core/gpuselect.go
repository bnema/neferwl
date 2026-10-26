package core

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bnema/neferwl/internal/ports"
)

// ErrNoRenderGPU means selection has no render-capable DRM device.
var ErrNoRenderGPU = errors.New("no render-capable DRM device")

// GPUSelectConfig selects startup DRM cards using resolved paths or PCI addresses.
type GPUSelectConfig struct {
	Devices []string // Empty means all candidates.
	Ignore  []string
	Render  string // The caller resolves configuration/environment precedence.
}

// SelectGPUs chooses cards and a render GPU without opening devices or resolving symlinks.
// Candidate order breaks ties and is preserved in Keep.
func SelectGPUs(c []ports.GPUInfo, cfg GPUSelectConfig) (ports.GPUSelection, error) {
	var out ports.GPUSelection
	resolve := func(ids []string) ([]bool, error) {
		selected := make([]bool, len(c))
		for _, id := range ids {
			found := false
			for i, g := range c {
				if matchesGPU(g, id) {
					selected[i], found = true, true
				}
			}
			if !found {
				return nil, fmt.Errorf("unknown DRM device %q", id)
			}
		}
		return selected, nil
	}
	allowed, err := resolve(cfg.Devices)
	if err != nil {
		return out, err
	}
	ignored, err := resolve(cfg.Ignore)
	if err != nil {
		return out, err
	}
	explicit := -1
	if cfg.Render != "" {
		for i, g := range c {
			if matchesGPU(g, cfg.Render) {
				explicit = i
				break
			}
		}
		if explicit < 0 {
			return out, fmt.Errorf("unknown DRM device %q", cfg.Render)
		}
	}
	kept := make([]bool, len(c))
	for i, g := range c {
		reason := "all devices"
		if len(cfg.Devices) != 0 {
			reason = "devices"
		}
		decision := "keep"
		switch {
		case ignored[i]:
			decision, reason = "ignore", "ignore"
		case len(cfg.Devices) != 0 && !allowed[i]:
			decision, reason = "ignore", "not in devices"
		default:
			kept[i] = true
			out.Keep = append(out.Keep, i)
		}
		out.Reasons = append(out.Reasons, fmt.Sprintf("%s GPU %s (%s): %s", decision, filepath.Base(g.Path), g.PCI, reason))
	}
	if explicit >= 0 && !kept[explicit] {
		return ports.GPUSelection{}, fmt.Errorf("render device %q is ignored", cfg.Render)
	}
	render, reason := -1, "lowest index"
	if explicit >= 0 {
		if c[explicit].Render != 0 {
			render, reason = explicit, "render.device"
		}
	} else {
		for _, i := range out.Keep {
			g := c[i]
			if g.Render == 0 {
				continue
			}
			if render < 0 {
				render = i
			}
			if g.Internal {
				render, reason = i, "internal"
				break
			}
		}
		if reason != "internal" {
			for _, i := range out.Keep {
				if c[i].Render != 0 && c[i].BootVGA {
					render, reason = i, "boot_vga"
					break
				}
			}
		}
	}
	if render < 0 {
		return ports.GPUSelection{}, ErrNoRenderGPU
	}
	out.Render = render
	g := c[render]
	out.Reasons = append(out.Reasons, fmt.Sprintf("render GPU %s (%s): %s", filepath.Base(g.Path), g.PCI, reason))
	return out, nil
}

func matchesGPU(g ports.GPUInfo, id string) bool {
	if strings.Contains(id, "/") {
		if id == g.Path {
			return true
		}
		// Linux dev_t stores minor bits 0..7 at 0..7 and 8..31 at 20..43.
		minor := (g.Render & 0xff) | ((g.Render >> 12) & 0xffffff00)
		return g.Render != 0 && id == fmt.Sprintf("/dev/dri/renderD%d", minor)
	}
	short := g.PCI
	if len(short) > 5 && short[4] == ':' {
		short = short[5:]
	}
	// A short address can match several domains: lists select all matches,
	// while explicit render selection uses the first candidate.
	return g.PCI != "" && (strings.EqualFold(id, g.PCI) || strings.EqualFold(id, short))
}
