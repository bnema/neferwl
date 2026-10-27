package vulkan

import (
	"image"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func hintGrid(cols, rows int) domain.PositionHint {
	h := domain.PositionHint{Opacity: 1, Columns: uint8(cols), ActiveCol: 1, ActiveRow: 1}
	for c := range cols {
		h.Counts[c] = uint8(rows)
	}
	return h
}

func BenchmarkPositionHintDraws(b *testing.B) {
	for _, g := range []struct {
		name       string
		cols, rows int
	}{{"5x3", 5, 3}, {"16x16", 16, 16}} {
		b.Run(g.name, func(b *testing.B) {
			r := &Renderer{width: 1920, height: 1080}
			s := domain.Scene{OutputWidth: 1920, OutputHeight: 1080, Scale: 1, PositionHint: hintGrid(g.cols, g.rows)}
			dmg := newDamage(&target{}, s, image.Rect(0, 0, r.width, r.height))
			b.ReportAllocs()
			var n int
			for b.Loop() {
				n = len(r.draws(s, nil, dmg))
			}
			b.ReportMetric(float64(n), "draws")
		})
	}
}
