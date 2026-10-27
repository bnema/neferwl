package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/bnema/neferwl/internal/adapters/drm"
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

func openPlannedRender(opener ports.RenderDeviceOpener, plans []core.CardPlan, strict bool, log zerowrap.Logger) (ports.RenderDevice, domain.GPUInfo, error) {
	ordered := core.RenderOpenOrder(plans, strict)
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
		errs = append(errs, core.ErrNoRenderGPU)
	}
	return nil, domain.GPUInfo{}, errors.Join(errs...)
}

// openHeadlessRender executes core policy, keeping filesystem probes at the edge.
func openHeadlessRender(opener ports.RenderDeviceOpener, cands []domain.GPUInfo, listErr error, cfg domain.Config, env string, log zerowrap.Logger) (ports.RenderDevice, error) {
	constrained := len(cfg.DRM.Devices) > 0 || len(cfg.DRM.Ignore) > 0 || cfg.Render.Device != "" || env != ""
	fallback := func(reason error) (ports.RenderDevice, error) {
		log.Info().Err(reason).Msg("render GPU: first usable Vulkan device (headless discovery fallback)")
		return opener.Open(domain.GPUInfo{})
	}
	config, err := drm.SelectConfig(cfg, env)
	if err != nil {
		return nil, err
	}
	matched := false
	if strings.HasPrefix(config.Render, "/dev/dri/renderD") && len(config.Devices) == 0 && len(config.Ignore) == 0 {
		for _, c := range cands {
			if core.MatchesGPU(c, config.Render) {
				matched = true
				break
			}
		}
	}
	decision := core.PlanHeadlessRender(cands, listErr, config, constrained, matched)
	switch decision.Choice {
	case core.HeadlessUseExplicitRenderNode:
		var st unix.Stat_t
		if err := unix.Stat(config.Render, &st); err != nil {
			return nil, fmt.Errorf("DRM device %q: %w", config.Render, err)
		}
		log.Info().Str("render", config.Render).Msg("render GPU: explicit render node without DRM card")
		return opener.Open(domain.GPUInfo{Render: st.Rdev})
	case core.HeadlessFallbackFirstDevice:
		return fallback(decision.Err)
	case core.HeadlessFail:
		return nil, decision.Err
	}
	for _, reason := range decision.Reasons {
		log.Info().Msg(reason)
	}
	dev, _, err := openPlannedRender(opener, decision.Plans, decision.Strict, log)
	if err != nil && decision.FallbackAfterFailure {
		return fallback(err)
	}
	return dev, err
}
