package core

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

type tile struct {
	name       string
	x, y, w, h int
}

// layoutCore builds a core whose screens sit at the given logical rectangles,
// in order, with the screen at index focus focused.
func layoutCore(focus int, tiles ...tile) *Core {
	c := &Core{focusScreen: focus}
	for _, t := range tiles {
		s := &screen{info: ports.OutputInfo{Name: t.name}, mon: newMonitor(t.name, ""), x: t.x, y: t.y}
		s.mon.SetOutput(t.w, t.h)
		c.screens = append(c.screens, s)
	}
	return c
}

func TestNeighbor(t *testing.T) {
	dirs := []direction{dirLeft, dirRight, dirUp, dirDown}
	dirNames := [...]string{"left", "right", "up", "down"}
	// want holds the expected neighbor name for left, right, up, down; "" is
	// the edge.
	type step struct {
		from string
		want [4]string
	}
	cases := []struct {
		name  string
		tiles []tile
		steps []step
	}{
		{"row of three in connection order", []tile{{"B", 200, 0, 200, 100}, {"A", 0, 0, 200, 100}, {"C", 400, 0, 200, 100}}, []step{
			{"A", [4]string{"", "B", "", ""}},
			{"B", [4]string{"A", "C", "", ""}},
			{"C", [4]string{"B", "", "", ""}},
		}},
		{"column of two", []tile{{"A", 0, 0, 200, 100}, {"B", 0, 100, 200, 100}}, []step{
			{"A", [4]string{"", "", "", "B"}},
			{"B", [4]string{"", "", "A", ""}},
		}},
		{"row with y offsets", []tile{{"A", 0, 0, 200, 100}, {"B", 200, 60, 200, 100}, {"C", 400, 120, 200, 100}}, []step{
			// A and C share no edge range: only diagonal vertical neighbors.
			{"A", [4]string{"", "B", "", "C"}},
			{"B", [4]string{"A", "C", "", ""}},
			{"C", [4]string{"B", "", "A", ""}},
		}},
		{"diagonal only", []tile{{"A", 0, 0, 100, 100}, {"B", 100, 150, 100, 100}}, []step{
			{"A", [4]string{"", "B", "", "B"}},
			{"B", [4]string{"A", "", "A", ""}},
		}},
		{"shared edge beats nearer diagonal", []tile{{"A", 0, 0, 100, 100}, {"B", 100, 150, 100, 100}, {"C", 300, 50, 100, 100}}, []step{
			{"A", [4]string{"", "C", "", "B"}},
		}},
		{"smaller gap wins", []tile{{"A", 0, 0, 100, 100}, {"far", 300, 0, 100, 100}, {"near", 150, 0, 100, 100}}, []step{
			{"A", [4]string{"", "near", "", ""}},
		}},
		{"larger shared edge wins at equal gap", []tile{{"A", 0, 0, 100, 100}, {"little", 100, 80, 100, 100}, {"big", 100, 20, 100, 60}}, []step{
			{"A", [4]string{"", "big", "", ""}},
		}},
		{"closer center wins at equal overlap", []tile{{"A", 0, 0, 100, 100}, {"low", 100, 50, 100, 100}, {"mid", 100, -20, 100, 70}}, []step{
			// Both share 50 rows; mid's center is nearer to A's.
			{"A", [4]string{"", "mid", "", ""}},
		}},
		{"lowest index at a full tie", []tile{{"A", 0, 0, 100, 100}, {"first", 100, 0, 100, 100}, {"second", 100, 0, 100, 100}}, []step{
			{"A", [4]string{"", "first", "", ""}},
		}},
		{"overlapping screens fall back to centers", []tile{{"A", 0, 0, 200, 100}, {"B", 100, 0, 200, 100}}, []step{
			// Centers are 100 and 200 apart on x only.
			{"A", [4]string{"", "B", "", ""}},
			{"B", [4]string{"A", "", "", ""}},
		}},
		{"outputs anchored to the same reference", []tile{{"R", 0, 0, 100, 100}, {"X", 100, 0, 100, 100}, {"Y", 100, 0, 300, 50}}, []step{
			// X and Y overlap; the strict pass finds nothing between them.
			{"X", [4]string{"R", "Y", "Y", ""}},
			// R is fully left of Y and wins over the closer, overlapping X.
			{"Y", [4]string{"R", "", "", "X"}},
		}},
		{"same-height outputs anchored to the same reference: one is unreachable by direction", []tile{{"R", 0, 0, 100, 100}, {"Y", 100, 0, 300, 100}, {"X", 100, 0, 100, 100}}, []step{
			{"R", [4]string{"", "Y", "", ""}},
			{"Y", [4]string{"R", "", "", ""}},
			{"X", [4]string{"R", "Y", "", ""}},
		}},
		{"strict candidate beats a closer overlapping one", []tile{{"A", 0, 0, 100, 100}, {"near", 50, 0, 100, 100}, {"far", 300, 0, 100, 100}}, []step{
			{"A", [4]string{"", "far", "", ""}},
			{"near", [4]string{"A", "far", "", ""}},
		}},
		{"identical centers are unreachable", []tile{{"A", 0, 0, 100, 100}, {"B", 0, 0, 100, 100}}, []step{
			{"A", [4]string{"", "", "", ""}},
			{"B", [4]string{"", "", "", ""}},
		}},
		{"placeholder ignored", []tile{{"A", 0, 0, 100, 100}, {"", 100, 0, 100, 100}, {"B", 300, 0, 100, 100}}, []step{
			{"A", [4]string{"", "B", "", ""}},
			{"B", [4]string{"A", "", "", ""}},
		}},
		{"two by three grid", []tile{
			{"DP-1", 0, 0, 2560, 1440}, {"DP-2", -1920, 360, 1920, 1080}, {"DP-3", 2560, 360, 1920, 1080},
			{"HDMI-1", 0, -1440, 2560, 1440}, {"DP-4", -1920, -1200, 1920, 1200}, {"DP-5", 2560, -1200, 1920, 1200},
		}, []step{
			{"DP-4", [4]string{"", "HDMI-1", "", "DP-2"}},
			{"HDMI-1", [4]string{"DP-4", "DP-5", "", "DP-1"}},
			{"DP-5", [4]string{"HDMI-1", "", "", "DP-3"}},
			{"DP-2", [4]string{"", "DP-1", "DP-4", ""}},
			{"DP-1", [4]string{"DP-2", "DP-3", "HDMI-1", ""}},
			{"DP-3", [4]string{"DP-1", "", "DP-5", ""}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, st := range tc.steps {
				c := layoutCore(0, tc.tiles...)
				for i, s := range c.screens {
					if s.name() == st.from {
						c.focusScreen = i
					}
				}
				for i, d := range dirs {
					got := ""
					if n := c.neighbor(d); n >= 0 {
						got = c.screens[n].name()
					}
					if got != st.want[i] {
						t.Errorf("from %s %s: got %q, want %q", st.from, dirNames[i], got, st.want[i])
					}
				}
			}
		})
	}
	if n := layoutCore(0, tile{"A", 0, 0, 10, 10}).neighbor(direction(0)); n != -1 {
		t.Fatalf("invalid direction: %d", n)
	}
}
