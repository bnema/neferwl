package core_test

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

var ultrawide = ports.OutputInfo{Name: "DP-3", Make: "Acme", Model: "W", Serial: "3", Width: 3440, Height: 1440}

// sizedRig runs an ultrawide with the named workspace "focus" (1920x1080)
// bound to Alt+w, and shows it.
func sizedRig(t *testing.T, overflow string, edit func(*ports.Config)) (*multiRig, ports.Scene) {
	t.Helper()
	r := startMulti(t, func(c *ports.Config) {
		c.Layout.Gaps = 0
		c.Layout.MaxColumns = 2
		c.Workspaces = []ports.WorkspaceConfig{{Name: "focus", Size: [2]int{1920, 1080}, LayoutRules: ports.LayoutRules{Overflow: overflow}}}
		c.Binds["Alt+w"] = "workspace focus"
		if edit != nil {
			edit(c)
		}
	}, ultrawide)
	return r, r.key(t, "w", ports.ModAlt)[0]
}

var frame = ports.Rect{X: 760, Y: 180, W: 1920, H: 1080}

func within(r, o ports.Rect) bool {
	return r.X >= o.X && r.Y >= o.Y && r.X+r.W <= o.X+o.W && r.Y+r.H <= o.Y+o.H
}

func TestWorkspaceSizeInheritsByDefault(t *testing.T) {
	r := startMulti(t, func(c *ports.Config) { c.Layout.Gaps = 0 }, ultrawide)
	set := r.mapWindow(t, 1)
	if set[0].WorkspaceClip != (ports.Rect{}) || set[0].Windows[0].Rect != (ports.Rect{W: 3440, H: 1440}) {
		t.Fatalf("%+v", set[0])
	}
}

func TestWorkspaceSizeScrollColumnsStayInFrame(t *testing.T) {
	r, _ := sizedRig(t, "scroll", nil)
	r.mapWindow(t, 1)
	r.mapWindow(t, 2)
	s := r.mapWindow(t, 3)[0]
	if s.WorkspaceClip != frame || s.OutputWidth != 3440 || s.OutputHeight != 1440 {
		t.Fatalf("clip %+v output %dx%d", s.WorkspaceClip, s.OutputWidth, s.OutputHeight)
	}
	for _, w := range s.Windows {
		if w.Hidden || !w.Rect.Overlaps(frame) {
			continue
		}
		if w.Rect.Y != frame.Y || w.Rect.H != frame.H {
			t.Fatalf("%+v not framed", w)
		}
	}
	// Focused column (last) ends at the frame's right edge, half the frame wide.
	last := s.Windows[2].Rect
	if last != (ports.Rect{X: 1720, Y: 180, W: 960, H: 1080}) {
		t.Fatal(last)
	}
}

func TestWorkspaceSizeFixedColumnsFillFrame(t *testing.T) {
	r, _ := sizedRig(t, "fixed", nil)
	r.mapWindow(t, 1)
	s := r.mapWindow(t, 2)[0]
	for i, want := range []ports.Rect{{X: 760, Y: 180, W: 960, H: 1080}, {X: 1720, Y: 180, W: 960, H: 1080}} {
		if s.Windows[i].Rect != want {
			t.Fatalf("%d: %+v", i, s.Windows[i])
		}
	}
}

func TestWorkspaceSizeFullscreenFillsFrame(t *testing.T) {
	r, _ := sizedRig(t, "scroll", nil)
	r.mapWindow(t, 1)
	r.mapWindow(t, 2)
	s := r.key(t, "f", ports.ModAlt|ports.ModShift)[0]
	full := 0
	for _, w := range s.Windows {
		if w.Fullscreen {
			full++
			if w.Rect != frame {
				t.Fatalf("%+v", w)
			}
		}
	}
	if full != 1 {
		t.Fatal(s.Windows)
	}
}

func TestWorkspaceSizeFixedFullscreenSiblingKeepsFrame(t *testing.T) {
	r, _ := sizedRig(t, "fixed", nil)
	r.mapWindow(t, 1)
	r.mapWindow(t, 2)
	s := r.key(t, "f", ports.ModAlt|ports.ModShift)[0]
	if s.WorkspaceClip != frame {
		t.Fatalf("sibling clip %+v", s.WorkspaceClip)
	}
	for _, w := range s.Windows {
		if w.Fullscreen && w.Rect != frame {
			t.Fatalf("%+v", w)
		}
	}
	// Reload keeps the origin's size on the sibling; removing it frees both.
	cfg := r.cfg
	r.reload <- ports.ConfigChanged{Config: cfg}
	if s := receive(t, r.scenes)[0]; s.WorkspaceClip != frame {
		t.Fatalf("after reload %+v", s.WorkspaceClip)
	}
	cfg.Workspaces = []ports.WorkspaceConfig{{Name: "focus", LayoutRules: ports.LayoutRules{Overflow: "fixed"}}}
	r.reload <- ports.ConfigChanged{Config: cfg}
	if s := receive(t, r.scenes)[0]; s.WorkspaceClip != (ports.Rect{}) {
		t.Fatalf("after size removal %+v", s.WorkspaceClip)
	}
}

func TestWorkspaceSizeFloatsContained(t *testing.T) {
	r, _ := sizedRig(t, "scroll", nil)
	r.mapWindow(t, 1)
	r.client <- ports.WindowMapped{ID: 2, Floating: true, Width: 5000, Height: 4000}
	s := receive(t, r.scenes)[0]
	for _, w := range s.Windows {
		if w.Floating && !within(w.Rect, frame) {
			t.Fatalf("float %+v outside %+v", w.Rect, frame)
		}
	}
}

func TestWorkspaceSizeReloadAndRemoval(t *testing.T) {
	r, _ := sizedRig(t, "scroll", nil)
	r.mapWindow(t, 1)
	cfg := r.cfg
	cfg.Workspaces = []ports.WorkspaceConfig{{Name: "focus", Size: [2]int{1000, 600}}}
	r.reload <- ports.ConfigChanged{Config: cfg}
	s := receive(t, r.scenes)[0]
	if want := (ports.Rect{X: 1220, Y: 420, W: 1000, H: 600}); s.WorkspaceClip != want || s.Windows[0].Rect != want {
		t.Fatalf("%+v %+v", s.WorkspaceClip, s.Windows[0])
	}
	// Larger than the monitor clamps to it: no clip, no invisible area.
	cfg.Workspaces = []ports.WorkspaceConfig{{Name: "focus", Size: [2]int{9000, 9000}}}
	r.reload <- ports.ConfigChanged{Config: cfg}
	s = receive(t, r.scenes)[0]
	if s.WorkspaceClip != (ports.Rect{}) || s.Windows[0].Rect != (ports.Rect{W: 3440, H: 1440}) {
		t.Fatalf("%+v %+v", s.WorkspaceClip, s.Windows[0])
	}
	cfg.Workspaces = []ports.WorkspaceConfig{{Name: "focus", Size: [2]int{1920, 1080}}}
	r.reload <- ports.ConfigChanged{Config: cfg}
	if s = receive(t, r.scenes)[0]; s.WorkspaceClip != frame {
		t.Fatal(s.WorkspaceClip)
	}
	cfg.Workspaces = []ports.WorkspaceConfig{{Name: "focus"}}
	r.reload <- ports.ConfigChanged{Config: cfg}
	if s = receive(t, r.scenes)[0]; s.WorkspaceClip != (ports.Rect{}) || s.Windows[0].Rect != (ports.Rect{W: 3440, H: 1440}) {
		t.Fatalf("%+v %+v", s.WorkspaceClip, s.Windows[0])
	}
}

func TestWorkspaceSizeFollowsOutputResize(t *testing.T) {
	r, _ := sizedRig(t, "scroll", nil)
	r.mapWindow(t, 1)
	smaller := ultrawide
	smaller.Width, smaller.Height = 1600, 900
	s := r.plug(t, smaller)[0]
	// 1920x1080 no longer fits: it clamps to the monitor.
	if s.WorkspaceClip != (ports.Rect{}) || s.Windows[0].Rect != (ports.Rect{W: 1600, H: 900}) {
		t.Fatalf("%+v %+v", s.WorkspaceClip, s.Windows[0])
	}
	s = r.plug(t, ultrawide)[0]
	if s.WorkspaceClip != frame || s.Windows[0].Rect != frame {
		t.Fatalf("%+v %+v", s.WorkspaceClip, s.Windows[0])
	}
}

func TestWorkspaceSizeReservedZoneIntersectsFrame(t *testing.T) {
	r, _ := sizedRig(t, "scroll", nil)
	// A bar inside the top of the monitor but above the frame reserves nothing of it.
	bar := ports.LayerSurface{ID: 9, Layer: ports.LayerTop, Anchor: ports.AnchorTop | ports.AnchorLeft | ports.AnchorRight, Height: 100, ExclusiveZone: 100}
	r.client <- ports.LayerChanged{Layers: []ports.LayerSurface{bar}}
	receive(t, r.scenes)
	s := r.mapWindow(t, 1)[0]
	if s.Windows[0].Rect != frame {
		t.Fatalf("%+v", s.Windows[0])
	}
	// A bar reaching into the frame shrinks it from the top.
	bar.Height, bar.ExclusiveZone = 300, 300
	r.client <- ports.LayerChanged{Layers: []ports.LayerSurface{bar}}
	s = receive(t, r.scenes)[0]
	if want := (ports.Rect{X: 760, Y: 300, W: 1920, H: 960}); s.Windows[0].Rect != want || s.WorkspaceClip != frame {
		t.Fatalf("%+v clip %+v", s.Windows[0], s.WorkspaceClip)
	}
}

func TestWorkspaceSizeFractionalScaleUnchanged(t *testing.T) {
	r, _ := sizedRig(t, "scroll", func(c *ports.Config) {
		c.Outputs = []ports.OutputConfig{{Name: "DP-3", Scale: 2}}
	})
	s := r.mapWindow(t, 1)[0]
	// The monitor is 1720x720 logical; a 1920x1080 workspace clamps to it.
	if s.OutputWidth != 1720 || s.OutputHeight != 720 || s.Scale != 2 || s.WorkspaceClip != (ports.Rect{}) {
		t.Fatalf("%dx%d scale %v clip %+v", s.OutputWidth, s.OutputHeight, s.Scale, s.WorkspaceClip)
	}
}

func TestWorkspaceSizeInputOutsideFrame(t *testing.T) {
	r, _ := sizedRig(t, "scroll", nil)
	r.mapWindow(t, 1)
	r.mapWindow(t, 2)
	for len(r.commands) > 0 {
		<-r.commands
	}
	// Inside the frame the window under the pointer gets the focus; window 1
	// is scrolled partly off but its visible part outside the frame is dead.
	r.input <- ports.PointerMotion{X: 2000, Y: 700}
	if v, ok := command(t, r.commands).(ports.PointerFocus); !ok || v.ID != 2 {
		t.Fatal(v)
	}
	r.input <- ports.PointerMotion{X: 100, Y: 700}
	for {
		if v, ok := command(t, r.commands).(ports.PointerFocus); ok {
			if v.ID != 0 {
				t.Fatalf("outside the frame: %+v", v)
			}
			break
		}
	}
}
