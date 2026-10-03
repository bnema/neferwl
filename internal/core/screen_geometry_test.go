package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func geometryTiles() []tile {
	return []tile{{"A", 0, 0, 200, 100}, {"B", 200, 0, 400, 200}}
}

func TestScreenAt(t *testing.T) {
	c := layoutCore(0, geometryTiles()...)
	for _, tc := range []struct {
		x, y float64
		want int
	}{
		{199, 50, 0}, {200, 50, 1}, {250, 150, 1}, {100, 150, -1}, {-1, 0, -1},
	} {
		assert.Equal(t, tc.want, c.screenAt(tc.x, tc.y), "(%v,%v)", tc.x, tc.y)
	}

	p := layoutCore(0, tile{"", 0, 0, 200, 100})
	assert.Equal(t, -1, p.screenAt(10, 10), "a placeholder is never returned")
}

func TestClampPointer(t *testing.T) {
	c := layoutCore(0, geometryTiles()...)
	x, y := c.clampPointer(0, 0, 100, 50)
	assert.Equal(t, [2]float64{100, 50}, [2]float64{x, y})
	x, y = c.clampPointer(100, 50, 100, 150)
	assert.Equal(t, [2]float64{100, 99}, [2]float64{x, y})
	x, y = c.clampPointer(-5, -5, -10, -10)
	assert.Equal(t, [2]float64{0, 0}, [2]float64{x, y})

	none := layoutCore(0, tile{"", 0, 0, 200, 100})
	x, y = none.clampPointer(0, 0, 500, 500)
	assert.Equal(t, [2]float64{500, 500}, [2]float64{x, y})
}

func TestScreenAtAllocations(t *testing.T) {
	c := layoutCore(0, geometryTiles()...)
	n := testing.AllocsPerRun(100, func() {
		c.screenAt(250, 150)
		c.clampPointer(100, 50, 100, 150)
	})
	assert.Zero(t, n)
}
