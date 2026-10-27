package ports

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestOutputPlacementToTarget(t *testing.T) {
	o := OutputPlacement{Info: OutputInfo{Width: 2560, Height: 1440}, Scale: 1, X: 100, Transform: 1}
	x, y := o.ToTarget(100, 0)
	wx, wy := domain.BufferTransform(1).ToBuffer(0, 0, 1440, 2560)
	assert.Equal(t, [2]float64{wx, wy}, [2]float64{x, y})
	assert.Equal(t, [2]float64{0, 1440}, [2]float64{x, y})

	o.Transform = 0
	x, y = o.ToTarget(150, 20)
	assert.Equal(t, [2]float64{50, 20}, [2]float64{x, y})
}
