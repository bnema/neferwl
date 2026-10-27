package core

import (
	"errors"
	"strings"

	"github.com/bnema/neferwl/internal/domain"
)

type CardPlan struct {
	Info   domain.GPUInfo
	Render bool
}

// PlanCards preserves the selector's candidate order; mode is irrelevant here.
func PlanCards(cands []domain.GPUInfo, sel domain.GPUSelection) []CardPlan {
	plans := make([]CardPlan, 0, len(sel.Keep))
	for _, i := range sel.Keep {
		plans = append(plans, CardPlan{Info: cands[i], Render: i == sel.Render})
	}
	return plans
}

// RenderOpenOrder returns the selected render GPU, followed by allowed fallbacks.
func RenderOpenOrder(plans []CardPlan, strict bool) []domain.GPUInfo {
	var ordered []domain.GPUInfo
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
	return ordered
}

type HeadlessRenderChoice int

const (
	HeadlessUseExplicitRenderNode HeadlessRenderChoice = iota
	HeadlessFallbackFirstDevice
	HeadlessUseSelection
	HeadlessFail
)

type HeadlessRenderDecision struct {
	Choice HeadlessRenderChoice
	Plans  []CardPlan
	Strict bool
	// FallbackAfterFailure permits unspecified Vulkan selection if opening Plans fails.
	FallbackAfterFailure bool
	Reasons              []string
	Err                  error
}

// PlanHeadlessRender decides discovery policy without probing device nodes.
func PlanHeadlessRender(cands []domain.GPUInfo, listErr error, cfg GPUSelectConfig, constrained, explicitRenderNodeMatched bool) HeadlessRenderDecision {
	if strings.HasPrefix(cfg.Render, "/dev/dri/renderD") && len(cfg.Devices) == 0 && len(cfg.Ignore) == 0 && !explicitRenderNodeMatched {
		return HeadlessRenderDecision{Choice: HeadlessUseExplicitRenderNode}
	}
	failure := func(err error) HeadlessRenderDecision {
		choice := HeadlessFail
		if !constrained {
			choice = HeadlessFallbackFirstDevice
		}
		return HeadlessRenderDecision{Choice: choice, Err: err}
	}
	if listErr != nil {
		return failure(listErr)
	}
	sel, err := SelectGPUs(cands, cfg)
	if err != nil {
		if errors.Is(err, ErrNoRenderGPU) {
			return failure(err)
		}
		return HeadlessRenderDecision{Choice: HeadlessFail, Err: err}
	}
	return HeadlessRenderDecision{Choice: HeadlessUseSelection, Plans: PlanCards(cands, sel), Strict: cfg.Render != "", FallbackAfterFailure: !constrained, Reasons: sel.Reasons}
}

// MatchesGPU matches a pre-resolved identifier, including a render node dev_t.
func MatchesGPU(g domain.GPUInfo, id string) bool { return matchesGPU(g, id) }
