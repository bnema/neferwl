package domain

import (
	"reflect"
	"testing"
)

func TestHintSourceString(t *testing.T) {
	for s, want := range map[HintSource]string{0: "", HintSourceATSPI: "atspi", HintSourceOCR: "ocr", HintSourceDetector: "detector"} {
		if s.String() != want {
			t.Fatal(s)
		}
	}
}

func TestHintWindowRegion(t *testing.T) {
	w := HintWindow{Rect: Rect{X: 10, Y: 10, W: 20, H: 20}, Popups: []Rect{{X: 40, Y: 0, W: 10, H: 15}, {X: -5, Y: 35, W: 10, H: 10}}}
	if !reflect.DeepEqual(w.Region(), []Rect{w.Rect, w.Popups[0], w.Popups[1]}) {
		t.Fatal(w.Region())
	}
	if got := w.Bounds(); got != (Rect{X: -5, Y: 0, W: 55, H: 45}) {
		t.Fatal(got)
	}
	for _, tc := range []struct {
		x, y int
		want bool
	}{{10, 10, true}, {29, 29, true}, {30, 20, false}, {40, 0, true}, {49, 14, true}, {50, 14, false}, {35, 20, false}, {-5, 35, true}} {
		if w.Contains(tc.x, tc.y) != tc.want {
			t.Fatal(tc)
		}
	}
	r := w.Region()
	r[0].X = 999
	r[1].X = 999
	if w.Rect.X != 10 || w.Popups[0].X != 40 {
		t.Fatal("region aliases window")
	}
	if (HintWindow{}).Bounds() != (Rect{}) || (HintWindow{}).Contains(0, 0) {
		t.Fatal("empty window")
	}
}
