package core

import (
	"image"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestPlaceOutputs(t *testing.T) {
	at := func(x, y int) *image.Point { return &image.Point{X: x, Y: y} }
	rel := func(r ports.OutputRelation, to string, off int) ports.OutputAnchor {
		return ports.OutputAnchor{Relation: r, To: to, Offset: off}
	}
	const (
		rightOf = ports.RelationRightOf
		leftOf  = ports.RelationLeftOf
		above   = ports.RelationAbove
		below   = ports.RelationBelow
	)
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
		{
			"automatic after explicit origins at both ends",
			[]placeItem{{name: "A", w: 400, h: 300, pos: at(1000, 0)}, {name: "B", w: 400, h: 300, pos: at(0, 0)}, {name: "C", w: 300, h: 300}},
			[]image.Point{{X: 1000, Y: 0}, {X: 0, Y: 0}, {X: 1400, Y: 0}},
		},
		{
			"automatic around an explicit origin",
			[]placeItem{{name: "A", w: 400, h: 300}, {name: "B", w: 800, h: 300, pos: at(0, 500)}, {name: "C", w: 300, h: 300}},
			[]image.Point{{X: 800, Y: 0}, {X: 0, Y: 500}, {X: 1200, Y: 0}},
		},
		{
			"right-of with offset",
			[]placeItem{
				{name: "DP-1", w: 2560, h: 1440},
				{name: "DP-2", w: 1920, h: 1080, anchor: rel(rightOf, "DP-1", 180)},
			},
			[]image.Point{{X: 0, Y: 0}, {X: 2560, Y: 180}},
		},
		{
			"left-of with negative offset",
			[]placeItem{
				{name: "DP-1", w: 2560, h: 1440, pos: at(100, 50)},
				{name: "DP-2", w: 1920, h: 1080, anchor: rel(leftOf, "DP-1", -30)},
			},
			[]image.Point{{X: 100, Y: 50}, {X: -1820, Y: 20}},
		},
		{
			"above",
			[]placeItem{
				{name: "DP-1", w: 2560, h: 1440},
				{name: "DP-2", w: 1920, h: 1080, anchor: rel(above, "DP-1", 40)},
			},
			[]image.Point{{X: 0, Y: 0}, {X: 40, Y: -1080}},
		},
		{
			"below",
			[]placeItem{
				{name: "DP-1", w: 2560, h: 1440},
				{name: "DP-2", w: 1920, h: 1080, anchor: rel(below, "DP-1", -40)},
			},
			[]image.Point{{X: 0, Y: 0}, {X: -40, Y: 1440}},
		},
		{
			"chain resolved against item order",
			[]placeItem{
				{name: "C", w: 100, h: 100, anchor: rel(rightOf, "B", 0)},
				{name: "B", w: 200, h: 100, anchor: rel(below, "A", 10)},
				{name: "A", w: 300, h: 150, pos: at(0, 0)},
			},
			[]image.Point{{X: 210, Y: 150}, {X: 10, Y: 150}, {X: 0, Y: 0}},
		},
		{
			"two by three grid",
			[]placeItem{
				{name: "DP-1", w: 2560, h: 1440},
				{name: "DP-2", w: 1920, h: 1080, anchor: rel(leftOf, "DP-1", 360)},
				{name: "DP-3", w: 1920, h: 1080, anchor: rel(rightOf, "DP-1", 360)},
				{name: "HDMI-1", w: 2560, h: 1440, anchor: rel(above, "DP-1", 0)},
				{name: "DP-4", w: 1920, h: 1200, anchor: rel(leftOf, "HDMI-1", 240)},
				{name: "DP-5", w: 1920, h: 1200, anchor: rel(rightOf, "HDMI-1", 240)},
			},
			[]image.Point{{X: 0, Y: 0}, {X: -1920, Y: 360}, {X: 2560, Y: 360}, {X: 0, Y: -1440}, {X: -1920, Y: -1200}, {X: 2560, Y: -1200}},
		},
		{
			"missing reference is automatic, after the plain automatic ones",
			[]placeItem{
				{name: "A", w: 400, h: 300, anchor: rel(rightOf, "GONE", 50)},
				{name: "B", w: 200, h: 300},
			},
			[]image.Point{{X: 200, Y: 0}, {X: 0, Y: 0}},
		},
		{
			"missing reference left of a negative origin stays right of zero",
			[]placeItem{
				{name: "A", w: 400, h: 300, pos: at(-900, 0)},
				{name: "B", w: 200, h: 300, anchor: rel(above, "GONE", 0)},
			},
			[]image.Point{{X: -900, Y: 0}, {X: 0, Y: 0}},
		},
		{
			"cycle is broken at its first output, the rest follows it",
			[]placeItem{
				{name: "A", w: 400, h: 300, anchor: rel(rightOf, "B", 0)},
				{name: "B", w: 200, h: 300, anchor: rel(below, "A", 0)},
			},
			[]image.Point{{X: 0, Y: 0}, {X: 0, Y: 300}},
		},
		{
			"explicit origin beats relation",
			[]placeItem{
				{name: "A", w: 400, h: 300},
				{name: "B", w: 200, h: 300, pos: at(7, 9), anchor: rel(rightOf, "A", 0)},
			},
			[]image.Point{{X: 207, Y: 0}, {X: 7, Y: 9}},
		},
		{
			"relation follows an explicit origin",
			[]placeItem{
				{name: "A", w: 400, h: 300, pos: at(1000, 200)},
				{name: "B", w: 200, h: 300, anchor: rel(rightOf, "A", 5)},
			},
			[]image.Point{{X: 1000, Y: 200}, {X: 1400, Y: 205}},
		},
		{
			"anchored output is not overlapped by a later automatic one",
			[]placeItem{
				{name: "A", w: 400, h: 300},
				{name: "B", w: 200, h: 300},
				{name: "C", w: 300, h: 300, anchor: rel(rightOf, "A", 0)},
			},
			[]image.Point{{X: 0, Y: 0}, {X: 700, Y: 0}, {X: 400, Y: 0}},
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
