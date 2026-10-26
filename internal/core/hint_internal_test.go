package core

import (
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
	"math"
	"testing"
	"testing/synctest"
)

func TestHintLabelGeometry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, scale := range []float64{1, 1.5} {
			sc := &screen{scale: scale, info: ports.OutputInfo{Width: 1500, Height: 1200}}
			sc.mon = newMonitor("", "")
			sc.mon.SetOutput(int(float64(sc.info.Width)/sc.scale), int(float64(sc.info.Height)/sc.scale))
			c := &Core{}
			c.cfg.Hint.Size = 200
			w, h := c.hintLabelSize(sc)
			font := math.Min(200, 256/scale)
			require.Equal(t, int(math.Ceil(1.2*font+6*font/13+2/scale+1)), w)
			require.Equal(t, int(math.Ceil(1.25*font+2*font/13+2/scale+1)), h)
			bounds := sc.rect()
			// Corner separation exceeds label width, but right-edge clamping folds
			// the second label back over the first.
			a := hintTarget{rect: ports.Rect{X: bounds.W - w - 1, Y: bounds.H - h - 1}, score: 1}
			b := hintTarget{rect: ports.Rect{X: bounds.W - 1, Y: bounds.H - 1}, score: .5}
			require.Len(t, c.hintSpread(sc, []hintTarget{a, b}), 1)
			b.rect = ports.Rect{X: 0, Y: 0}
			require.Len(t, c.hintSpread(sc, []hintTarget{a, b}), 2)
		}

	})
}

func TestHintSpreadBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sc := &screen{scale: 1, info: ports.OutputInfo{Width: 1000000, Height: 1000}}
		sc.mon = newMonitor("", "")
		sc.mon.SetOutput(int(float64(sc.info.Width)/sc.scale), int(float64(sc.info.Height)/sc.scale))
		c := &Core{}
		ts := make([]hintTarget, 4*hintMaxTargets+10)
		for i := range ts {
			ts[i] = hintTarget{rect: ports.Rect{X: 100 + i*100, Y: 100}, score: float32(i)}
		}
		got := c.hintSpread(sc, ts)
		require.Len(t, got, 4*hintMaxTargets)
		require.Equal(t, 4*hintMaxTargets, cap(got))
		require.Equal(t, float32(0), ts[0].score) // Selection must not reorder the caller's slice.
		require.Equal(t, float32(len(ts)-1), got[0].score)
		require.Equal(t, float32(10), got[len(got)-1].score)

	})
}
