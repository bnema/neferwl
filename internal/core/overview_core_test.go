package core_test

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// outputScene returns the scene of the named output in set.
func outputScene(t *testing.T, set []ports.Scene, name string) ports.Scene {
	t.Helper()
	for _, s := range set {
		if s.Output == name {
			return s
		}
	}
	t.Fatalf("no scene for %s", name)
	return ports.Scene{}
}

// scenePreview returns window id's overview preview in s.
func scenePreview(s ports.Scene, id ports.WindowID) (ports.SceneWindow, bool) {
	for _, w := range s.Windows {
		if w.ID == id && w.Preview > 0 {
			return w, true
		}
	}
	return ports.SceneWindow{}, false
}

func hasPreview(s ports.Scene) bool {
	for _, w := range s.Windows {
		if w.Preview > 0 {
			return true
		}
	}
	return false
}

// noButtonSent fails if core forwarded a click to a client.
func noButtonSent(t *testing.T, ch chan ports.ClientCommand) {
	t.Helper()
	for len(ch) > 0 {
		if v, ok := (<-ch).(ports.PointerButtonTo); ok {
			t.Fatalf("click reached a client: %+v", v)
		}
	}
}

func TestOverviewCoreClickPicksOnPointerOutput(t *testing.T) {
	r := startMulti(t, nil, left, right)
	r.mapWindow(t, 1)
	r.key(t, "Right", ports.ModAlt|ports.ModCtrl)
	r.mapWindow(t, 2)
	r.mapWindow(t, 3)
	set := r.key(t, "o", ports.ModAlt)
	p, ok := scenePreview(outputScene(t, set, "DP-2"), 2)
	if !ok {
		t.Fatal("no preview of window 2 on DP-2")
	}
	for len(r.commands) > 0 {
		<-r.commands
	}
	r.input <- ports.PointerMotion{X: float64(left.Width + p.Rect.X + p.Rect.W/2), Y: float64(p.Rect.Y + p.Rect.H/2)}
	r.input <- ports.PointerButton{Button: 0x110, Pressed: true}
	deadline := time.After(time.Second)
	for picked := false; !picked; {
		select {
		case set := <-r.scenes:
			s := outputScene(t, set, "DP-2")
			for _, w := range s.Windows {
				if w.ID == 2 && w.Preview == 0 && w.Focused && !w.Hidden {
					picked = true
				}
			}
		case <-deadline:
			t.Fatal("window 2 not picked")
		}
	}
	noButtonSent(t, r.commands)
}

func TestOverviewCoreClickOutsidePreviewKeepsOverview(t *testing.T) {
	r := startMulti(t, nil, right)
	r.mapWindow(t, 1)
	r.mapWindow(t, 2)
	set := r.key(t, "o", ports.ModAlt)
	for _, w := range set[0].Windows {
		if w.Preview > 0 && w.Rect.X <= 1 && w.Rect.Y <= 1 && w.Rect.X+w.Rect.W > 1 && w.Rect.Y+w.Rect.H > 1 {
			t.Fatalf("(1, 1) is inside preview %+v", w)
		}
	}
	for len(r.commands) > 0 {
		<-r.commands
	}
	r.input <- ports.PointerMotion{X: 1, Y: 1}
	r.input <- ports.PointerButton{Button: 0x110, Pressed: true}
	r.input <- ports.PointerButton{Button: 0x110}
	// h moves the selection in the overview; it would be a plain key
	// otherwise, with no scene.
	r.input <- ports.KeyEvent{Keysym: "h", Pressed: true}
	select {
	case s := <-r.scenes:
		if !hasPreview(s[0]) {
			t.Fatal("overview closed by a click outside the previews")
		}
	case <-time.After(time.Second):
		t.Fatal("overview closed by a click outside the previews: h published nothing")
	}
	noButtonSent(t, r.commands)
}

func TestOverviewCoreEscapeRestoresFocus(t *testing.T) {
	r := startMulti(t, nil, right)
	r.mapWindow(t, 1)
	r.mapWindow(t, 2)
	r.mapWindow(t, 3)
	r.key(t, "o", ports.ModAlt)
	r.key(t, "h", 0)
	set := r.key(t, "h", 0)
	if p, ok := scenePreview(set[0], 1); !ok || !p.Focused {
		t.Fatalf("preview 1 not selected: %+v", set[0].Windows)
	}
	set = r.key(t, "Escape", 0)
	if hasPreview(set[0]) {
		t.Fatal("overview still open")
	}
	for _, w := range set[0].Windows {
		if w.Focused != (w.ID == 3) {
			t.Fatalf("focus: %+v", w)
		}
	}
}

// The default move bind moves the selected preview and keeps the overview
// open, through the same path as any key.
func TestOverviewCoreMoveBind(t *testing.T) {
	r := startMulti(t, nil, right)
	r.mapWindow(t, 1)
	r.mapWindow(t, 2)
	r.mapWindow(t, 3)
	r.key(t, "o", ports.ModAlt)
	r.key(t, "h", 0)
	if set := r.key(t, "Next", ports.ModAlt|ports.ModShift); !hasPreview(set[0]) {
		t.Fatal("overview closed")
	}
	r.key(t, "Escape", 0)
	// The state channel keeps the latest snapshot: wait for the one after
	// Escape, back on window 3 of workspace 1. The overview showed window
	// 2 in the row below; closed, it is off screen on workspace 2.
	deadline := time.After(time.Second)
	for {
		select {
		case st := <-r.state:
			if st.Window == nil || st.Window.ID != 3 || st.Outputs[0].Active != 1 {
				continue
			}
			for _, w := range st.Windows {
				if w.ID == 2 && w.Workspace == 2 && !w.Visible {
					return
				}
			}
		case <-deadline:
			t.Fatal("window 2 not on workspace 2 after escape")
		}
	}
}
