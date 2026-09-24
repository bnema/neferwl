package core

import (
	"reflect"
	"testing"
)

func TestCleanScales(t *testing.T) {
	for _, tc := range []struct {
		w, h int
		want []float64
	}{
		{5120, 2160, []float64{1, 1.25, 4.0 / 3, 1.6, 5.0 / 3, 2, 2.5, 8.0 / 3}},
		{3840, 2160, []float64{1, 1.2, 1.25, 4.0 / 3, 1.5, 1.6, 5.0 / 3, 1.875, 2, 2.4, 2.5, 8.0 / 3, 3}},
	} {
		if got := CleanScales(tc.w, tc.h); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%dx%d: %v", tc.w, tc.h, got)
		}
	}
}

func TestStepScale(t *testing.T) {
	if s := StepScale(5120, 2160, 1, 1); s != 1.25 {
		t.Fatal(s)
	}
	// 1.5 is not clean on 5K: up goes to 1.6, down to 4/3.
	if s := StepScale(5120, 2160, 1.5, 1); s != 1.6 {
		t.Fatal(s)
	}
	if s := StepScale(5120, 2160, 1.5, -1); s != 4.0/3 {
		t.Fatal(s)
	}
	if s := StepScale(5120, 2160, 1, -1); s != 1 {
		t.Fatal(s)
	}
	if s := StepScale(1920, 1080, 3, 1); s != 3 {
		t.Fatal(s)
	}
}

func TestSnapScale(t *testing.T) {
	for in, want := range map[float64]float64{0: 1, -2: 1, 1.5: 1.5, 1.3333: 160.0 / 120, 9: 4} {
		if got := SnapScale(in); got != want {
			t.Errorf("%v: %v", in, got)
		}
	}
}
