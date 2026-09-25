package wayland

import (
	"testing"

	"github.com/bnema/nefertty/internal/ports"
)

// High-resolution wheels send fractions of a detent: pre-v8 clients get a
// discrete step once 120 builds up; a direction change drops the rest.
func TestWheelSteps(t *testing.T) {
	s := &Server{}
	wheel := func(v, h int32) [2]int32 {
		return s.wheelSteps(ports.PointerAxis{
			Vertical:   ports.ScrollAxis{Set: v != 0, V120: v},
			Horizontal: ports.ScrollAxis{Set: h != 0, V120: h},
		})
	}
	for i, tc := range []struct {
		v, h int32
		want [2]int32
	}{
		{60, 0, [2]int32{}},
		{60, 0, [2]int32{1, 0}},
		{180, 0, [2]int32{1, 0}},
		{60, 0, [2]int32{1, 0}},
		{-60, 240, [2]int32{0, 2}},
		{-60, 0, [2]int32{-1, 0}},
		{30, -30, [2]int32{}},
	} {
		if got := wheel(tc.v, tc.h); got != tc.want {
			t.Fatalf("step %d: got %v, want %v", i, got, tc.want)
		}
	}
	if s.wheelSteps(ports.PointerAxis{Source: ports.AxisFinger, Vertical: ports.ScrollAxis{Set: true, V120: 240}}) != [2]int32{} {
		t.Fatal("finger scroll made discrete steps")
	}
}
