package core

import (
	"image"
	"slices"
	"testing"
)

func TestPlaceOutputs(t *testing.T) {
	at := func(x, y int) *image.Point { return &image.Point{X: x, Y: y} }
	tests := []struct {
		name  string
		items []placeItem
		want  []image.Point
	}{
		{"none", nil, []image.Point{}},
		{"one automatic", []placeItem{{name: "A", w: 400, h: 300}}, []image.Point{{X: 0, Y: 0}}},
		{
			"three automatic",
			[]placeItem{{name: "A", w: 400, h: 300}, {name: "B", w: 200, h: 300}, {name: "C", w: 300, h: 300}},
			[]image.Point{{X: 0, Y: 0}, {X: 400, Y: 0}, {X: 600, Y: 0}},
		},
		{
			"explicit position then automatic",
			[]placeItem{{name: "DP-1", w: 400, h: 300}, {name: "DP-2", w: 400, h: 300, pos: at(100, 40)}},
			[]image.Point{{X: 500, Y: 0}, {X: 100, Y: 40}},
		},
		{
			"explicit negative x leaves automatic at zero",
			[]placeItem{{name: "A", w: 300, h: 300}, {name: "B", w: 400, h: 300, pos: at(-500, 0)}},
			[]image.Point{{X: 0, Y: 0}, {X: -500, Y: 0}},
		},
		{
			"all explicit with negative position",
			[]placeItem{{name: "A", w: 400, h: 300, pos: at(-200, 12)}, {name: "B", w: 400, h: 300, pos: at(20, -120)}},
			[]image.Point{{X: -200, Y: 12}, {X: 20, Y: -120}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := placeOutputs(tc.items); !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
