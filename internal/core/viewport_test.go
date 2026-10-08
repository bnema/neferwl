package core

import (
	"context"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// A tile is seen in the usable area; a float, a fullscreen window and a
// preview are placed against the whole frame.
func TestViewportShows(t *testing.T) {
	v := viewport{frame: Rect{W: 100, H: 80}, usable: Rect{W: 100, H: 60}}
	under := Rect{Y: 60, W: 50, H: 60} // the next cascade band, under a bottom panel
	for _, tc := range []struct {
		name string
		v    viewport
		p    Placement
		want bool
	}{
		{"tile in usable", v, Placement{Rect: Rect{W: 50, H: 60}}, true},
		{"tile under panel", v, Placement{Rect: under}, false},
		{"tile across the panel line", v, Placement{Rect: Rect{Y: 30, W: 50, H: 60}}, true},
		{"tile off frame", v, Placement{Rect: Rect{Y: 80, W: 50, H: 60}}, false},
		{"hidden tile", v, Placement{Rect: Rect{W: 50, H: 60}, Hidden: true}, false},
		{"float under panel", v, Placement{Rect: under, Floating: true}, true},
		{"hidden float", v, Placement{Rect: Rect{W: 50, H: 60}, Floating: true, Hidden: true}, false},
		{"fullscreen tile", v, Placement{Rect: Rect{W: 100, H: 80}, Fullscreen: true}, true},
		{"preview under panel", v, Placement{Rect: under, Preview: 0.5}, true},
		{"tile with no usable area", viewport{frame: v.frame}, Placement{Rect: Rect{W: 50, H: 60}}, false},
		{"float with no usable area", viewport{frame: v.frame}, Placement{Rect: Rect{W: 50, H: 60}, Floating: true}, true},
	} {
		if got := tc.v.shows(tc.p); got != tc.want {
			t.Errorf("%s: shows %v, want %v", tc.name, got, tc.want)
		}
	}
}

// viewportCore is a core with one 100x80 output whose usable area is
// usable, publishing into the returned check: it asserts what each
// window's configure and the state snapshot say about its visibility.
func viewportCore(t *testing.T, layout func(*ports.Config), usable Rect) (*Core, func(map[WindowID]bool) ports.Scene) {
	t.Helper()
	cfg := ports.Config{}
	cfg.Keyboard.CmdKey = "super"
	layout(&cfg)
	commands := make(chan ports.ClientCommand, 256)
	scenes := make(chan []ports.Scene, 1)
	c, err := New(cfg, Channels{Commands: commands, Scenes: scenes}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c.addScreen(ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80})
	c.cur().mon.SetUsable(usable)
	sent := map[WindowID]ports.ConfigureWindow{}
	check := func(want map[WindowID]bool) ports.Scene {
		t.Helper()
		if err := c.publish(context.Background()); err != nil {
			t.Fatal(err)
		}
		scene := (<-scenes)[0]
		for len(commands) > 0 {
			if v, ok := (<-commands).(ports.ConfigureWindow); ok {
				sent[v.ID] = v
			}
		}
		state := map[WindowID]bool{}
		for _, s := range c.state().Windows {
			state[s.ID] = s.Visible
		}
		for id, vis := range want {
			if v := sent[id]; v.Visible != vis {
				t.Fatalf("window %d: configure %+v, want visible %v", id, v, vis)
			}
			if state[id] != vis {
				t.Fatalf("window %d: state visible %v, want %v", id, state[id], vis)
			}
		}
		return scene
	}
	return c, check
}

// The next cascade band starts right under a bottom panel: its windows are
// not seen, so they are told they are invisible (and throttled), while the
// scene still carries them.
func TestConfigureVisibleCascadeUnderPanel(t *testing.T) {
	c, check := viewportCore(t, func(cfg *ports.Config) {
		cfg.Layout.Overflow = "cascade"
		cfg.Layout.MaxColumns = 2
	}, Rect{W: 100, H: 60})
	m := c.cur().mon
	w := m.Current()
	for id := WindowID(1); id <= 3; id++ {
		m.AddWindow(id)
	}
	w.FocusID(1)
	scene := check(map[WindowID]bool{1: true, 2: true, 3: false})
	found := false
	for _, sw := range scene.Windows {
		found = found || sw.ID == 3
	}
	if !found {
		t.Fatal("window under the panel left the scene")
	}
	// It is cut at the panel and takes no pointer there.
	if scene.TileClip != (Rect{W: 100, H: 60}) {
		t.Fatalf("tile clip %+v, want the usable area", scene.TileClip)
	}
	if id, _, _ := c.hit(10, 70); id != 0 {
		t.Fatalf("pointer over the panel went to window %d", id)
	}
	if id, _, _ := c.hit(10, 30); id != 1 {
		t.Fatalf("pointer over window 1 went to %d", id)
	}
	// Its band scrolled in, the first band is under nothing but off screen.
	w.FocusID(3)
	check(map[WindowID]bool{1: false, 2: false, 3: true})
	// Fullscreen covers the panel: always seen.
	w.FocusID(1)
	check(map[WindowID]bool{1: true, 3: false})
	w.FocusID(3)
	w.ToggleFullscreen()
	check(map[WindowID]bool{3: true})
}

// A column scrolled under a left panel is not seen.
func TestConfigureVisibleScrollUnderSidePanel(t *testing.T) {
	c, check := viewportCore(t, func(cfg *ports.Config) {
		cfg.Layout.MaxColumns = 1
	}, Rect{X: 20, W: 80, H: 80})
	m := c.cur().mon
	m.AddWindow(1)
	m.AddWindow(2)
	if r := m.Layout()[0].Rect; r.X >= 20 || r.X+r.W <= 0 {
		t.Fatalf("column 1 at %+v, want partly under the panel", r)
	}
	check(map[WindowID]bool{1: false, 2: true})
}
