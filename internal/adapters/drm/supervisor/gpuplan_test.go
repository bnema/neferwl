package supervisor

import (
	"errors"
	"testing"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
)

func TestGPUSelectionErrorBeforePlan(t *testing.T) {
	policy := newMockgpuPlanner(t)
	candidates := []domain.GPUInfo{{Path: "/dev/dri/card0"}}
	cfg := core.GPUSelectConfig{Render: "missing"}
	selectionErr := errors.New("selection failed")
	// No Plan expectation: planning after failed selection fails this test.
	policy.EXPECT().Select(candidates, cfg).Return(domain.GPUSelection{}, selectionErr).Once()
	_, plans, err := selectAndPlanGPUs(policy, candidates, cfg)
	if !errors.Is(err, selectionErr) || plans != nil {
		t.Fatalf("selection failure: plans=%v err=%v", plans, err)
	}
}
