package geom

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestLogical(t *testing.T) {
	for _, tc := range []struct {
		physical int
		scale    float64
		want     int
	}{
		{3840, 1.5, 2560},
		{5, 2, 3},
		{-5, 2, -3},
		{0, 1.25, 0},
	} {
		if got := Logical(tc.physical, tc.scale); got != tc.want {
			t.Errorf("Logical(%d, %v) = %d, want %d", tc.physical, tc.scale, got, tc.want)
		}
	}
}

func TestPlacementPeeking(t *testing.T) {
	p := Placement{ID: ports.WindowID(7), Rect: ports.Rect{X: 1, Y: 2, W: 30, H: 40}, Focused: true}
	want := p
	want.Peek, want.Veil = true, 0.4
	p.Peeking(0.4)
	if p != want {
		t.Fatalf("Peeking changed unrelated placement fields: got %+v, want %+v", p, want)
	}
	p.Peeking(1)
	if !p.Peek || p.Veil != 1 {
		t.Fatalf("Peeking did not update veil: %+v", p)
	}
}

func TestBorderZone(t *testing.T) {
	for _, tc := range []struct {
		a, b, pad, origin, size int
	}{
		{10, 10, 3, 7, 6},
		{10, 20, 2, 10, 10},
		{10, 12, 5, 6, 10},
		{10, 10, 0, 10, 1},
	} {
		origin, size := BorderZone(tc.a, tc.b, tc.pad)
		if origin != tc.origin || size != tc.size {
			t.Errorf("BorderZone(%d, %d, %d) = (%d, %d), want (%d, %d)", tc.a, tc.b, tc.pad, origin, size, tc.origin, tc.size)
		}
	}
}
