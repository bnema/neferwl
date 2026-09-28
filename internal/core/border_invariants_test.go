package core_test

import (
	"math/rand"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// Published scenes, not just layouts: exercise the real owner loop, binds,
// monitor transfers, unmaps, and both kinds of overflow with a fixed seed.
func TestBorderSceneInvariants(t *testing.T) {
	for _, overflow := range []string{"fixed", "scroll"} {
		for _, gap := range []int{0, 6} {
			t.Run(overflow+map[int]string{0: "-flush", 6: "-gaps"}[gap], func(t *testing.T) {
				rng := rand.New(rand.NewSource(20260928))
				r := startMulti(t, func(c *ports.Config) {
					c.Border.Width = 2
					c.Layout.MaxColumns = 3
					c.Layout.Overflow = overflow
					c.Layout.Gaps = gap
				}, left, right)
				var ids []ports.WindowID
				next := ports.WindowID(1)
				// A single keypress may publish a scene even when the action is
				// inapplicable; check every resulting scene independently.
				keys := []struct {
					sym  string
					mods ports.Mods
				}{
					{"Left", ports.ModAlt | ports.ModShift}, {"Right", ports.ModAlt | ports.ModShift},
					{"Left", ports.ModAlt}, {"Right", ports.ModAlt},
					{"Left", ports.ModAlt | ports.ModCtrl}, {"Right", ports.ModAlt | ports.ModCtrl},
					{"Left", ports.ModAlt | ports.ModCtrl | ports.ModShift}, {"Right", ports.ModAlt | ports.ModCtrl | ports.ModShift},
					{"bracketleft", ports.ModAlt}, {"bracketright", ports.ModAlt},
					{"f", ports.ModAlt}, {"f", ports.ModAlt | ports.ModShift},
					{"s", ports.ModAlt}, {"S", ports.ModAlt | ports.ModShift}, {"Up", ports.ModAlt}, {"Down", ports.ModAlt},
				}
				for step := range 180 {
					var scenes []ports.Scene
					switch {
					case len(ids) < 2 || rng.Intn(5) == 0 && len(ids) < 12:
						scenes = r.mapWindow(t, next)
						ids = append(ids, next)
						next++
					case rng.Intn(9) == 0:
						i := rng.Intn(len(ids))
						r.client <- ports.WindowUnmapped{ID: ids[i]}
						scenes = receive(t, r.scenes)
						ids = append(ids[:i], ids[i+1:]...)
					default:
						k := keys[rng.Intn(len(keys))]
						scenes = r.key(t, k.sym, k.mods)
					}
					checkBorderScenes(t, step, gap, scenes)
				}
			})
		}
	}
}

func checkBorderScenes(t *testing.T, step, gap int, scenes []ports.Scene) {
	t.Helper()
	focused := 0
	for _, s := range scenes {
		checkBorderScene(t, step, gap, s)
		for _, w := range s.Windows {
			if w.Focused && !w.Hidden && !w.Popup && w.Rect.Overlaps(ports.Rect{W: s.OutputWidth, H: s.OutputHeight}) {
				focused++
			}
		}
	}
	if focused > 1 {
		t.Fatalf("step %d: focused windows on multiple outputs: %+v", step, scenes)
	}
}

func checkBorderScene(t *testing.T, step, gap int, s ports.Scene) {
	t.Helper()
	o := ports.Rect{W: s.OutputWidth, H: s.OutputHeight}
	var focus *ports.SceneWindow
	tiles := 0
	for i := range s.Windows {
		w := &s.Windows[i]
		if w.Popup || w.Hidden || !w.Rect.Overlaps(o) {
			continue
		}
		if w.Focused {
			if focus != nil {
				t.Fatalf("step %d %s: two focused windows: %+v", step, s.Output, s.Windows)
			}
			focus = w
		}
		if !w.Floating && !w.Fullscreen {
			tiles++
		}
		if w.Fullscreen && w.Inset != 0 {
			t.Fatalf("step %d %s: fullscreen inset: %+v", step, s.Output, w)
		}
		if !w.Floating && !w.Fullscreen {
			if got, want := w.Inset, wantInset(s, *w, gap); got != want {
				t.Fatalf("step %d %s: tile %d inset %v, want %v; scene %+v", step, s.Output, w.ID, got, want, s)
			}
		}
	}
	if tiles <= 1 {
		for _, w := range s.Windows {
			if !w.Floating && !w.Popup && !w.Hidden && w.Rect.Overlaps(o) && w.Inset != 0 {
				t.Fatalf("step %d %s: lone tile reserves a border: %+v; scene %+v", step, s.Output, w, s)
			}
		}
	}
	for _, sep := range s.Separators {
		r := sep.Rect
		if r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > o.W || r.Y+r.H > o.H {
			t.Fatalf("step %d %s: line outside output: %+v; scene %+v", step, s.Output, sep, s)
		}
		if sep.Window != 0 {
			found := false
			for _, w := range s.Windows {
				if w.ID == sep.Window && w.Floating && !w.Fullscreen && !w.Hidden && w.Rect.Overlaps(r) {
					found = true
				}
			}
			if !found {
				t.Fatalf("step %d %s: line belongs to invisible float: %+v; scene %+v", step, s.Output, sep, s)
			}
		} else if tiles < 2 {
			t.Fatalf("step %d %s: lone tile has shared line: %+v; scene %+v", step, s.Output, sep, s)
		}
		if sep.Active && (focus == nil || focus.Fullscreen || sep.Window != 0 && sep.Window != focus.ID || sep.Window == 0 && focus.Floating) {
			t.Fatalf("step %d %s: lit line without matching focused tile/float: %+v; scene %+v", step, s.Output, sep, s)
		}
	}
	// A focused tile with a visible neighbor lights at least one line
	// touching it.
	if focus == nil || focus.Floating || focus.Fullscreen || neighborSides(s, *focus, gap) == 0 {
		return
	}
	near := ports.Rect{X: focus.Rect.X - 2, Y: focus.Rect.Y - 2, W: focus.Rect.W + 4, H: focus.Rect.H + 4}
	for _, sep := range s.Separators {
		if sep.Active && sep.Window == 0 && sep.Rect.Overlaps(near) {
			return
		}
	}
	t.Fatalf("step %d %s: focused tile %d has neighbors but no lit line; scene %+v", step, s.Output, focus.ID, s)
}

// neighborSides are the sides of tile w facing another visible tile across
// gap, with the shared edge strictly inside the output.
func neighborSides(s ports.Scene, w ports.SceneWindow, gap int) ports.Sides {
	o := ports.Rect{W: s.OutputWidth, H: s.OutputHeight}
	var sides ports.Sides
	for _, other := range s.Windows {
		if other.ID == w.ID || other.Popup || other.Floating || other.Fullscreen || other.Hidden || !other.Rect.Overlaps(o) {
			continue
		}
		a, b := w.Rect, other.Rect
		rows := a.Y < b.Y+b.H && b.Y < a.Y+a.H
		cols := a.X < b.X+b.W && b.X < a.X+a.W
		switch {
		case rows && a.X+a.W+gap == b.X && a.X+a.W > 0 && a.X+a.W < o.W:
			sides |= ports.SideRight
		case rows && b.X+b.W+gap == a.X && a.X > 0 && a.X < o.W:
			sides |= ports.SideLeft
		case cols && a.Y+a.H+gap == b.Y && a.Y+a.H > 0 && a.Y+a.H < o.H:
			sides |= ports.SideBottom
		case cols && b.Y+b.H+gap == a.Y && a.Y > 0 && a.Y < o.H:
			sides |= ports.SideTop
		}
	}
	return sides
}

// wantInset is the exact inset of a visible tile: every neighbor side with
// gaps, only the right and bottom (shared-line owner) sides without.
func wantInset(s ports.Scene, w ports.SceneWindow, gap int) ports.Sides {
	sides := neighborSides(s, w, gap)
	if gap == 0 {
		sides &= ports.SideRight | ports.SideBottom
	}
	return sides
}

// Regression for the original two-monitor transfer: when the last tile
// comes back, neither the old output nor the new one has a shared line.
func TestBorderMoveBackAlone(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) {
		c.Border.Width = 2
		c.Layout.MaxColumns = 3
		c.Layout.Overflow = "fixed"
	}, left, right)
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl)
	r.mapWindow(t, 1)
	for _, sym := range []string{"Left", "Right"} {
		scenes := r.key(t, sym, ports.ModAlt|ports.ModShift)
		checkBorderScenes(t, 0, 0, scenes)
		if got := shown(scenes); len(got["DP-1"]) != map[string]int{"Left": 1, "Right": 0}[sym] || len(got["DP-2"]) != map[string]int{"Left": 0, "Right": 1}[sym] {
			t.Fatalf("%s: window did not move: %v", sym, got)
		}
	}
}
