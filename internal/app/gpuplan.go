package app

import (
	"errors"
	"fmt"
	"github.com/bnema/zerowrap"
	"path/filepath"
	"strings"

	"github.com/bnema/neferwl/internal/adapters/drm"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/ports"
)

type cardPlan struct {
	Info   ports.GPUInfo
	Render bool
}

// planCards preserves the selector's candidate order; mode is irrelevant here.
func planCards(cands []ports.GPUInfo, sel ports.GPUSelection) []cardPlan {
	plans := make([]cardPlan, 0, len(sel.Keep))
	for _, i := range sel.Keep {
		plans = append(plans, cardPlan{Info: cands[i], Render: i == sel.Render})
	}
	return plans
}

func resolveIDs(ids []string) ([]string, error) {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id
		if strings.Contains(id, "/") {
			path, err := filepath.EvalSymlinks(id)
			if err != nil {
				return nil, fmt.Errorf("DRM device %q: %w", id, err)
			}
			out[i] = path
		}
	}
	return out, nil
}

// gpuSelectConfig takes the environment value explicitly, keeping precedence
// testable without reading process-global state. Filesystem resolution is I/O.
func gpuSelectConfig(cfg ports.Config, envRender string) (core.GPUSelectConfig, error) {
	var out core.GPUSelectConfig
	var err error
	if out.Devices, err = resolveIDs(cfg.DRM.Devices); err != nil {
		return out, err
	}
	if out.Ignore, err = resolveIDs(cfg.DRM.Ignore); err != nil {
		return out, err
	}
	render := cfg.Render.Device
	if envRender != "" {
		render = envRender
	}
	if render != "" {
		ids, err := resolveIDs([]string{render})
		if err != nil {
			return out, err
		}
		out.Render = ids[0]
	}
	return out, nil
}

// Configuration parsing has already validated the value; zero means auto.
func multiGPUMode(value string) drm.MultiGPU {
	switch value {
	case "direct":
		return drm.MultiGPUDirect
	case "off":
		return drm.MultiGPUOff
	default:
		return drm.MultiGPUAuto
	}
}

func selectGPUPlans(cands []ports.GPUInfo, cfg ports.Config, env string) ([]cardPlan, ports.GPUSelection, error) {
	config, err := gpuSelectConfig(cfg, env)
	if err != nil {
		return nil, ports.GPUSelection{}, err
	}
	sel, err := core.SelectGPUs(cands, config)
	if err != nil {
		return nil, sel, err
	}
	return planCards(cands, sel), sel, nil
}

func openPlannedRender(opener ports.RenderDeviceOpener, plans []cardPlan, strict bool, log zerowrap.Logger) (ports.RenderDevice, ports.GPUInfo, error) {
	var ordered []ports.GPUInfo
	for _, p := range plans {
		if p.Render {
			ordered = append(ordered, p.Info)
		}
	}
	if !strict {
		for _, p := range plans {
			if !p.Render && p.Info.Render != 0 {
				ordered = append(ordered, p.Info)
			}
		}
	}
	var errs []error
	for _, info := range ordered {
		dev, err := opener.Open(info)
		if err == nil {
			return dev, info, nil
		}
		log.Warn().Err(err).Str("card", info.Path).Msg("render GPU unavailable")
		errs = append(errs, fmt.Errorf("render card %s: %w", info.Path, err))
	}
	if len(errs) == 0 {
		errs = append(errs, errors.New("no render-capable DRM card"))
	}
	return nil, ports.GPUInfo{}, errors.Join(errs...)
}
