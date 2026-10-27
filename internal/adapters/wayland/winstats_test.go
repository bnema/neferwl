package wayland

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/assert"
)

func statsRingOf(samples ...uint32) *frameRing {
	r := &frameRing{}
	for _, s := range samples {
		r.push(s)
	}
	return r
}

func statsRepeat(us uint32, n int) []uint32 {
	out := make([]uint32, n)
	for i := range out {
		out[i] = us
	}
	return out
}

func TestFrameRingSummary(t *testing.T) {
	spike := append(statsRepeat(10000, 999), 50000)
	wrapped := append(statsRepeat(50000, 500), statsRepeat(10000, domain.StatsFrames)...)
	capped := []uint32{70000, 100000, 20000}

	tests := []struct {
		name    string
		samples []uint32
		want    frameSummary
		fps     float32
		graph   func(g *[domain.StatsGraph]uint16)
	}{
		{name: "empty"},
		{
			name:    "constant",
			samples: statsRepeat(10000, 200),
			want:    frameSummary{Avg: 100, Min: 100, Max: 100, Low1: 100, Low01: 100},
			fps:     100,
			graph: func(g *[domain.StatsGraph]uint16) {
				for i := range g {
					g[i] = 10000
				}
			},
		},
		{
			name:    "spike",
			samples: spike,
			want: frameSummary{
				Avg: float32(1e6 / (float64(999*10000+50000) / 1000)),
				Min: 20, Max: 100,
				Low1:  float32(1e6 / (float64(9*10000+50000) / 10)),
				Low01: 20,
			},
			fps: float32(1e6 / (float64(999*10000+50000) / 1000)),
			graph: func(g *[domain.StatsGraph]uint16) {
				for i := range g {
					g[i] = 10000
				}
				g[domain.StatsGraph-1] = 50000
			},
		},
		{
			name:    "wrap keeps last frames",
			samples: wrapped,
			want:    frameSummary{Avg: 100, Min: 100, Max: 100, Low1: 100, Low01: 100},
			fps:     100,
			graph: func(g *[domain.StatsGraph]uint16) {
				for i := range g {
					g[i] = 10000
				}
			},
		},
		{
			name:    "cap and front padding",
			samples: capped,
			want: frameSummary{
				Avg: float32(1e6 / (190000.0 / 3)),
				Min: 10, Max: 50, Low1: 10, Low01: 10,
			},
			fps: float32(1e6 / (190000.0 / 3)),
			graph: func(g *[domain.StatsGraph]uint16) {
				g[domain.StatsGraph-3] = 65535
				g[domain.StatsGraph-2] = 65535
				g[domain.StatsGraph-1] = 20000
			},
		},
		{name: "zero ignored", samples: []uint32{0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := statsRingOf(tt.samples...)
			want := tt.want
			if tt.graph != nil {
				tt.graph(&want.Graph)
			}
			var scratch [domain.StatsFrames]uint32
			assert.Equal(t, want, r.summary(&scratch))
			assert.Equal(t, tt.fps, r.fps())
		})
	}
}

func TestFrameRingWrapHoldsLast1024(t *testing.T) {
	r := statsRingOf(append(statsRepeat(50000, 500), statsRepeat(10000, domain.StatsFrames)...)...)
	assert.Equal(t, domain.StatsFrames, r.n)
}

func TestFrameRingCapOrder(t *testing.T) {
	// 70000 and 100000 exceed uint16: capped; 20000 stays.
	r := statsRingOf(70000, 100000, 20000)
	var scratch [domain.StatsFrames]uint32
	s := r.summary(&scratch)
	assert.Equal(t, [3]uint16{65535, 65535, 20000}, [3]uint16(s.Graph[domain.StatsGraph-3:]))
	assert.Zero(t, s.Graph[0])
}

func TestFrameRingSummaryAllocs(t *testing.T) {
	r := statsRingOf(statsRepeat(12000, 3000)...)
	var scratch [domain.StatsFrames]uint32
	assert.Zero(t, testing.AllocsPerRun(100, func() { _ = r.summary(&scratch) }))
}

func TestWindowStatsScanoutReasons(t *testing.T) {
	w := windowStats{watched: 1}
	for _, reason := range []string{"overlays", "other_windows", "no_fullscreen", ""} {
		f := ports.FlipInfo{Shows: map[domain.WindowID]uint64{1: 1}, ScanoutReason: reason, OverlayReason: "overlays"}
		if reason == "" {
			f.ZeroCopy = 1
		}
		w.flip(&f)
		got := w.snapshot()
		assert.Equal(t, reason, got.ScanoutReason)
		assert.Equal(t, "overlays", got.OverlayReason)
		assert.Equal(t, reason == "", got.Scanout)
	}
	w.flip(&ports.FlipInfo{ScanoutReason: "unrelated output"})
	assert.Empty(t, w.snapshot().ScanoutReason)
}
