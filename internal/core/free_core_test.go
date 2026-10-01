package core_test

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func windowIn(set []ports.Scene, output string, id ports.WindowID) (ports.SceneWindow, bool) {
	for _, s := range set {
		if s.Output != output {
			continue
		}
		for _, w := range s.Windows {
			if w.ID == id && !w.Hidden {
				return w, true
			}
		}
	}
	return ports.SceneWindow{}, false
}

func TestFreeFloatKeepsPlaceAcrossMonitors(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) { c.Layout.Gaps = 0 }, left, right)
	r.mapWindow(t, 1)
	r.mapWindow(t, 2)
	r.key(t, "space", ports.ModAlt|ports.ModShift)
	// Nudge left twice: 100 px left of centre on a 200 px output stops at
	// the edge.
	r.key(t, "Left", ports.ModAlt|ports.ModShift)
	set := r.key(t, "Left", ports.ModAlt|ports.ModShift)
	before, ok := windowIn(set, "DP-1", 2)
	if !ok || !before.Floating || before.Rect.X != 0 {
		t.Fatalf("%+v", before)
	}
	// The workspace moves to the 400x200 output: the centre keeps its
	// fractions of the usable area (1/4, 1/2), the size stays.
	set = r.key(t, "Right", ports.ModAlt|ports.ModCtrl|ports.ModShift)
	after, ok := windowIn(set, "DP-2", 2)
	if !ok || after.Rect.W != before.Rect.W || after.Rect.X+after.Rect.W/2 != 100 || after.Rect.Y+after.Rect.H/2 != 100 {
		t.Fatalf("%+v", after)
	}
}
