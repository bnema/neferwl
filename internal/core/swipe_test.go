package core_test

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func (r *multiRig) swipe(t *testing.T, d ports.SwipeDir) []ports.Scene {
	t.Helper()
	for len(r.commands) > 0 {
		<-r.commands
	}
	r.input <- ports.Swipe{Dir: d}
	return receive(t, r.scenes)
}

// lastFocus returns the last window core focused since the swipe.
func (r *multiRig) lastFocus() ports.WindowID {
	var id ports.WindowID
	for len(r.commands) > 0 {
		if v, ok := (<-r.commands).(ports.FocusWindow); ok {
			id = v.ID
		}
	}
	return id
}

func TestSwipeChangesWorkspace(t *testing.T) {
	for _, natural := range []bool{false, true} {
		r := startMulti(t, func(c *ports.Config) { c.Touchpad.NaturalScroll = natural }, right)
		r.mapWindow(t, 1)
		down, up := ports.SwipeDown, ports.SwipeUp
		if natural {
			down, up = up, down
		}
		if got := shown(r.swipe(t, down))["DP-2"]; len(got) != 0 {
			t.Fatalf("natural=%t: next workspace shows %v", natural, got)
		}
		if got := shown(r.swipe(t, up))["DP-2"]; len(got) != 1 || got[0] != 1 {
			t.Fatalf("natural=%t: back shows %v", natural, got)
		}
	}
}

func TestSwipeChangesColumn(t *testing.T) {
	for _, overflow := range []string{"scroll", "fixed"} {
		for _, natural := range []bool{false, true} {
			r := startMulti(t, func(c *ports.Config) {
				c.Layout.Overflow = overflow
				c.Touchpad.NaturalScroll = natural
			}, right)
			r.mapWindow(t, 1)
			r.mapWindow(t, 2)
			left, right := ports.SwipeLeft, ports.SwipeRight
			if natural {
				left, right = right, left
			}
			r.swipe(t, left)
			if got := r.lastFocus(); got != 1 {
				t.Fatalf("%s natural=%t: left focuses %d", overflow, natural, got)
			}
			r.swipe(t, right)
			if got := r.lastFocus(); got != 2 {
				t.Fatalf("%s natural=%t: right focuses %d", overflow, natural, got)
			}
		}
	}
}
