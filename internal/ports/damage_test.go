package ports

import (
	"slices"
	"testing"
)

func TestDamageSince(t *testing.T) {
	a, b := Rect{X: 1, W: 2, H: 2}, Rect{Y: 5, W: 1, H: 1}
	c := SurfaceContent{Seq: 7, DamageHistory: []SeqDamage{{Seq: 5, Rects: []Rect{a}}, {Seq: 6, Full: true}, {Seq: 7, Rects: []Rect{b}}}}
	for _, tc := range []struct {
		name  string
		since uint64
		want  []Rect
		ok    bool
	}{
		{"up to date", 7, nil, true},
		{"one behind", 6, []Rect{b}, true},
		{"across a full change", 5, nil, false},
		{"history too short", 3, nil, false},
		{"first content", 0, nil, false},
	} {
		got, ok := c.DamageSince(tc.since)
		if ok != tc.ok || !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v %v", tc.name, got, ok)
		}
	}
	c.DamageHistory[1] = SeqDamage{Seq: 6, Rects: []Rect{a}}
	if got, ok := c.DamageSince(4); !ok || !slices.Equal(got, []Rect{a, a, b}) {
		t.Fatalf("union %v %v", got, ok)
	}
	// A history not ending at the content is stale: redraw all.
	c.Seq = 8
	if _, ok := c.DamageSince(6); ok {
		t.Fatal("stale history used")
	}
}
