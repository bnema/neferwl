package vulkan

import (
	"image"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func BenchmarkFlashSceneWalk(b *testing.B) {
	r := &Renderer{width: 1000, height: 600}
	w := sceneWalk{r: r, s: ports.Scene{Flash: &ports.FocusFlash{Rect: domain.Rect{X: 10, Y: 10, W: 300, H: 200}, Width: 4, Color: "#ff0000", Alpha: 0.5}}, scale: 1, zoom: 1, alpha: 1, bounds: image.Rect(0, 0, 1000, 600), draws: make([]draw, 0, 4)}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		w.draws = w.draws[:0]
		w.focusFlash()
	}
}
