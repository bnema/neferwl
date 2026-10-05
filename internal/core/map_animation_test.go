package core_test

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// A window that maps through the owner loop appears: the scene right after
// the map shows it invisible at 90 %, a frame later it is in between, and it
// settles exactly; its neighbour slides over. The client is configured once
// per window. With animations off the first scene is the final one.
func TestMapAnimates(t *testing.T) {
	r := startSwipe(t, nil)
	r.mapWindow(t, 1)
	for len(r.commands) > 0 {
		<-r.commands
	}
	first := r.multiRig.mapWindow(t, 2)[0]
	w := windowOf(t, first, 2)
	if w.Fade != 1 || w.Hidden {
		t.Fatalf("new window right after the map: %+v", w)
	}
	if got := rectNow(t, first, 1); got.W != 800 {
		t.Fatalf("neighbour right after the map is %d wide, want its old 800", got.W)
	}
	mid := r.frame(t, 30*time.Millisecond)
	if m := windowOf(t, mid, 2); !(m.Fade > 0 && m.Fade < 1 && m.Rect.W > w.Rect.W && m.Rect.W < 400) {
		t.Fatalf("new window on the first frame: %+v (was %+v)", m, w)
	}
	if got := rectNow(t, mid, 1).W; got <= 400 || got >= 800 {
		t.Fatalf("neighbour on the first frame is %d wide, want between 400 and 800", got)
	}
	end := r.settle(t)
	if m := windowOf(t, end, 2); m.Fade != 0 || m.Zoom != 0 || m.Rect.W != 400 {
		t.Fatalf("settled new window: %+v", m)
	}
	if got := rectNow(t, end, 1).W; got != 400 {
		t.Fatalf("settled neighbour is %d wide", got)
	}
	configures := map[ports.WindowID]int{}
	for len(r.commands) > 0 {
		if v, ok := (<-r.commands).(ports.ConfigureWindow); ok {
			configures[v.ID]++
			if v.Width != 400 {
				t.Fatalf("configure to %d wide, want the final 400: %+v", v.Width, v)
			}
		}
	}
	if configures[2] != 1 || configures[1] != 1 {
		t.Fatalf("configures %v, want one each", configures)
	}
}

// With animations off a map shows the final layout at once.
func TestMapAnimationsOffIsInstant(t *testing.T) {
	r := startSwipe(t, animationsOff)
	r.mapWindow(t, 1)
	s := r.multiRig.mapWindow(t, 2)[0]
	if w := windowOf(t, s, 2); w.Fade != 0 || w.Zoom != 0 || w.Rect.W != 400 {
		t.Fatalf("new window: %+v", w)
	}
	if got := rectNow(t, s, 1).W; got != 400 {
		t.Fatalf("neighbour is %d wide, want 400", got)
	}
	r.noFrameScene(t)
}

// A window that goes fullscreen right after its map drops its entrance.
func TestMapFullscreenRequestDropsEntrance(t *testing.T) {
	r := startSwipe(t, nil)
	s := r.multiRig.mapWindow(t, 1)[0]
	if windowOf(t, s, 1).Fade != 1 {
		t.Fatal("no entrance to drop")
	}
	r.client <- ports.WindowFullscreenRequest{ID: 1, Fullscreen: true, External: true}
	s = scene(t, r.scenes)
	if w := windowOf(t, s, 1); w.Fade != 0 || w.Zoom != 0 || !w.Fullscreen {
		t.Fatalf("fullscreen window still fading: %+v", w)
	}
}

// A fullscreen request inside the grace after the map is ignored (Wine at a
// remembered size): the entrance goes on.
func TestMapFullscreenRequestWithinGraceKeepsEntrance(t *testing.T) {
	r := startSwipe(t, nil)
	s := r.multiRig.mapWindow(t, 1)[0]
	if windowOf(t, s, 1).Fade != 1 {
		t.Fatal("no entrance to keep")
	}
	r.client <- ports.WindowFullscreenRequest{ID: 1, Fullscreen: true}
	// The flip is a barrier: the request is handled, a leftover scene dropped.
	set, ok := r.clk.flip(t, r.frames, r.scenes, 30*time.Millisecond, wide.Name)
	if !ok {
		t.Fatal("no frame: the entrance stopped")
	}
	if w := windowOf(t, set[0], 1); w.Fullscreen || !(w.Fade > 0 && w.Fade < 1) {
		t.Fatalf("ignored fullscreen request changed the window: %+v", w)
	}
}

func windowOf(t *testing.T, s ports.Scene, id ports.WindowID) ports.SceneWindow {
	t.Helper()
	for _, w := range s.Windows {
		if w.ID == id {
			return w
		}
	}
	t.Fatalf("window %d not in %+v", id, s.Windows)
	return ports.SceneWindow{}
}
