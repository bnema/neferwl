package core_test

import (
	"errors"
	"testing"

	"github.com/bnema/neferwl/internal/core"
)

func TestSelectGPUsNoRenderSentinel(t *testing.T) {
	_, err := core.SelectGPUs(nil, core.GPUSelectConfig{})
	if !errors.Is(err, core.ErrNoRenderGPU) {
		t.Fatalf("selection error: %v", err)
	}
}
