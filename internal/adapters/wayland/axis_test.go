package wayland

import (
	"testing"

	"github.com/bnema/nefertty/internal/ports"
)

// High-resolution wheels send fractions of a detent: pre-v8 clients get a
// discrete step once 120 builds up; a direction change drops the rest.
func TestWheelSteps(t *testing.T) {
	s := &Server{}
	var values [2]float64
	wheel := func(v, h int32) (steps [2]int32) {
		steps, values = s.wheelSteps(ports.PointerAxis{
			Vertical:   ports.ScrollAxis{Set: v != 0, V120: v, Value: float64(v) / 8},
			Horizontal: ports.ScrollAxis{Set: h != 0, V120: h},
		})
		return steps
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
	// The held value sums the frames since the last step.
	*s = Server{}
	wheel(60, 0)
	if s.wheelHeld[0] != 7.5 {
		t.Fatal("held", s.wheelHeld[0])
	}
	if wheel(60, 0) != [2]int32{1, 0} || s.wheelHeld[0] != 0 || values[0] != 15 {
		t.Fatal("held after step", s.wheelHeld[0])
	}
	// A direction change drops what was held the other way.
	wheel(60, 0)
	if wheel(-180, 0) != [2]int32{-1, 0} || values[0] != -22.5 {
		t.Fatal("after reversal", values)
	}
	if steps, _ := s.wheelSteps(ports.PointerAxis{Source: ports.AxisFinger, Vertical: ports.ScrollAxis{Set: true, V120: 240}}); steps != [2]int32{} {
		t.Fatal("finger scroll made discrete steps")
	}
}
