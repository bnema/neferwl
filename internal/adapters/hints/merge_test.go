package hints

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/require"
)

func tg(x, y, w, h int, score float32) ports.HintTarget {
	return ports.HintTarget{Rect: domain.Rect{X: x, Y: y, W: w, H: h}, Score: score}
}

func TestMergeTargets(t *testing.T) {
	tests := []struct {
		name string
		in   []ports.HintTarget
		want []ports.HintTarget
	}{
		{"empty", nil, []ports.HintTarget{}},
		{"duplicate keeps the best score", []ports.HintTarget{tg(10, 10, 100, 40, 0.3), tg(12, 11, 100, 40, 0.9)}, []ports.HintTarget{tg(12, 11, 100, 40, 0.9)}},
		{"duplicate, input order irrelevant", []ports.HintTarget{tg(12, 11, 100, 40, 0.9), tg(10, 10, 100, 40, 0.3)}, []ports.HintTarget{tg(12, 11, 100, 40, 0.9)}},
		{"row with icon and text keeps both small ones", []ports.HintTarget{
			tg(0, 0, 400, 30, 0.95), tg(4, 4, 22, 22, 0.5), tg(40, 6, 200, 18, 0.4),
		}, []ports.HintTarget{tg(4, 4, 22, 22, 0.5), tg(40, 6, 200, 18, 0.4)}},
		{"nested keeps the smaller even with a lower score", []ports.HintTarget{tg(0, 0, 200, 100, 0.9), tg(20, 20, 50, 30, 0.1)}, []ports.HintTarget{tg(20, 20, 50, 30, 0.1)}},
		{"partly overlapping distinct buttons are kept", []ports.HintTarget{tg(0, 0, 100, 30, 0.8), tg(80, 0, 100, 30, 0.7)}, []ports.HintTarget{tg(0, 0, 100, 30, 0.8), tg(80, 0, 100, 30, 0.7)}},
		{"mostly (not 85%) inside is kept", []ports.HintTarget{tg(0, 0, 100, 100, 0.8), tg(60, 0, 100, 20, 0.7)}, []ports.HintTarget{tg(0, 0, 100, 100, 0.8), tg(60, 0, 100, 20, 0.7)}},
		{"chain of three nested keeps the innermost", []ports.HintTarget{tg(0, 0, 300, 200, 0.99), tg(10, 10, 200, 100, 0.5), tg(20, 20, 50, 30, 0.2)}, []ports.HintTarget{tg(20, 20, 50, 30, 0.2)}},
		{"disjoint", []ports.HintTarget{tg(0, 0, 10, 10, 0.2), tg(50, 50, 10, 10, 0.6)}, []ports.HintTarget{tg(50, 50, 10, 10, 0.6), tg(0, 0, 10, 10, 0.2)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, mergeTargets(tc.in))
		})
	}
}
