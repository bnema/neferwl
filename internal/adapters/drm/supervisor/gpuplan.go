package supervisor

import (
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
)

// gpuPlanner keeps the startup selection-before-planning ordering testable.
type gpuPlanner interface {
	Select([]domain.GPUInfo, core.GPUSelectConfig) (domain.GPUSelection, error)
	Plan([]domain.GPUInfo, domain.GPUSelection) []core.CardPlan
}

type gpuPolicy struct{}

func (gpuPolicy) Select(c []domain.GPUInfo, cfg core.GPUSelectConfig) (domain.GPUSelection, error) {
	return core.SelectGPUs(c, cfg)
}
func (gpuPolicy) Plan(c []domain.GPUInfo, sel domain.GPUSelection) []core.CardPlan {
	return core.PlanCards(c, sel)
}

func selectAndPlanGPUs(policy gpuPlanner, c []domain.GPUInfo, cfg core.GPUSelectConfig) (domain.GPUSelection, []core.CardPlan, error) {
	sel, err := policy.Select(c, cfg)
	if err != nil {
		return domain.GPUSelection{}, nil, err
	}
	return sel, policy.Plan(c, sel), nil
}
