package core_test

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

var ultrawide = domain.OutputInfo{Name: "DP-3", Make: "Acme", Model: "W", Serial: "3", Width: 3440, Height: 1440}

// sizedRig runs an ultrawide with the named workspace "focus" (1920x1080)
// bound to Alt+w, and shows it. The animations setting is the default's
// unless edit changes it.
func sizedRig(t *testing.T, overflow string, edit func(*domain.Config)) (*multiRig, domain.Scene) {
	t.Helper()
	r := startMulti(t, sizedConfig(overflow, edit), ultrawide)
	return r, r.key(t, "w", domain.ModAlt)[0]
}

// sizedLanding is sizedRig with animations forced on or off; the scene it
// returns and every keyLanded and mapLanded of the rig are settled.
func sizedLanding(t *testing.T, animated bool, overflow string, edit func(*domain.Config)) (*landRig, domain.Scene) {
	t.Helper()
	r := startLanding(t, animated, sizedConfig(overflow, edit), ultrawide)
	return r, r.keyLanded(t, "w", domain.ModAlt)[0]
}

var frame = domain.Rect{X: 760, Y: 180, W: 1920, H: 1080}

func within(r, o domain.Rect) bool {
	return r.X >= o.X && r.Y >= o.Y && r.X+r.W <= o.X+o.W && r.Y+r.H <= o.Y+o.H
}

func TestWorkspaceSizeInheritsByDefault(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		r := startLanding(t, animated, func(c *domain.Config) { c.Layout.Gaps = 0 }, ultrawide)
		set := r.mapLanded(t, 1)
		if set[0].WorkspaceClip != (domain.Rect{}) || set[0].Windows[0].Rect != (domain.Rect{W: 3440, H: 1440}) {
			t.Fatalf("%+v", set[0])
		}
	})
}

func TestWorkspaceSizeScrollColumnsStayInFrame(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		r, _ := sizedLanding(t, animated, "scroll", nil)
		workspaceSizeScrollColumnsStayInFrame(t, r)
	})
}

func TestWorkspaceSizeFixedColumnsFillFrame(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		r, _ := sizedLanding(t, animated, "fixed", nil)
		workspaceSizeFixedColumnsFillFrame(t, r)
	})
}

func workspaceSizeFixedColumnsFillFrame(t *testing.T, r *landRig) {
	r.mapLanded(t, 1)
	s := r.mapLanded(t, 2)[0]
	for i, want := range []domain.Rect{{X: 760, Y: 180, W: 960, H: 1080}, {X: 1720, Y: 180, W: 960, H: 1080}} {
		if s.Windows[i].Rect != want {
			t.Fatalf("%d: %+v", i, s.Windows[i])
		}
	}
}

func TestWorkspaceSizeFullscreenFillsFrame(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		r, _ := sizedLanding(t, animated, "scroll", nil)
		r.mapLanded(t, 1)
		r.mapLanded(t, 2)
		s := r.keyLanded(t, "f", domain.ModAlt|domain.ModShift)[0]
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
	})
}

func TestWorkspaceSizeFixedFullscreenKeepsFrame(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		r, _ := sizedLanding(t, animated, "fixed", nil)
		r.mapLanded(t, 1)
		r.mapLanded(t, 2)
		s := r.keyLanded(t, "f", domain.ModAlt|domain.ModShift)[0]
		if s.WorkspaceClip != frame {
			t.Fatalf("fullscreen clip %+v", s.WorkspaceClip)
		}
		for _, w := range s.Windows {
			if w.Fullscreen && w.Rect != frame {
				t.Fatalf("%+v", w)
			}
		}
		// Reload keeps the size; removing it frees the workspace.
		cfg := r.cfg
		r.reload <- domain.ConfigChanged{Config: cfg}
		if s := r.land(receive(t, r.scenes))[0]; s.WorkspaceClip != frame {
			t.Fatalf("after reload %+v", s.WorkspaceClip)
		}
		cfg.Workspaces = []domain.WorkspaceConfig{{Name: "focus", LayoutRules: domain.LayoutRules{Overflow: "fixed"}}}
		r.reload <- domain.ConfigChanged{Config: cfg}
		if s := r.land(receive(t, r.scenes))[0]; s.WorkspaceClip != (domain.Rect{}) {
			t.Fatalf("after size removal %+v", s.WorkspaceClip)
		}
	})
}

func TestWorkspaceSizeFloatsContained(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _ := sizedRig(t, "scroll", nil)
		r.mapWindow(t, 1)
		r.client <- ports.WindowMapped{ID: 2, Floating: true, Width: 5000, Height: 4000}
		s := receive(t, r.scenes)[0]
		for _, w := range s.Windows {
			if w.Floating && !within(w.Rect, frame) {
				t.Fatalf("float %+v outside %+v", w.Rect, frame)
			}
		}

	})
}

func TestWorkspaceSizeReloadAndRemoval(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		r, _ := sizedLanding(t, animated, "scroll", nil)
		workspaceSizeReloadAndRemoval(t, r)
	})
}

func TestWorkspaceSizeFollowsOutputResize(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		r, _ := sizedLanding(t, animated, "scroll", nil)
		workspaceSizeFollowsOutputResize(t, r)
	})
}

func TestWorkspaceSizeReservedZoneIntersectsFrame(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		r, _ := sizedLanding(t, animated, "scroll", nil)
		workspaceSizeReservedZoneIntersectsFrame(t, r)
	})
}

func TestWorkspaceSizeFractionalScaleUnchanged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _ := sizedRig(t, "scroll", func(c *domain.Config) {
			c.Outputs = []domain.OutputConfig{{Name: "DP-3", Scale: 2}}
		})
		s := r.mapWindow(t, 1)[0]
		// The monitor is 1720x720 logical; a 1920x1080 workspace clamps to it.
		if s.OutputWidth != 1720 || s.OutputHeight != 720 || s.Scale != 2 || s.WorkspaceClip != (domain.Rect{}) {
			t.Fatalf("%dx%d scale %v clip %+v", s.OutputWidth, s.OutputHeight, s.Scale, s.WorkspaceClip)
		}

	})
}

func TestWorkspaceSizeInputOutsideFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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

	})
}

func sizedConfig(overflow string, edit func(*domain.Config)) func(*domain.Config) {
	return func(c *domain.Config) {
		c.Layout.Gaps = 0
		c.Layout.MaxColumns = 2
		c.Workspaces = []domain.WorkspaceConfig{{Name: "focus", Size: [2]int{1920, 1080}, LayoutRules: domain.LayoutRules{Overflow: overflow}}}
		c.Binds["Alt+w"] = "workspace focus"
		if edit != nil {
			edit(c)
		}
	}
}
func workspaceSizeScrollColumnsStayInFrame(t *testing.T, r *landRig) {
	r.mapLanded(t, 1)
	r.mapLanded(t, 2)
	s := r.mapLanded(t, 3)[0]
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
	if last != (domain.Rect{X: 1720, Y: 180, W: 960, H: 1080}) {
		t.Fatal(last)
	}
}
func workspaceSizeReloadAndRemoval(t *testing.T, r *landRig) {
	r.mapLanded(t, 1)
	cfg := r.cfg
	cfg.Workspaces = []domain.WorkspaceConfig{{Name: "focus", Size: [2]int{1000, 600}}}
	r.reload <- domain.ConfigChanged{Config: cfg}
	s := receive(t, r.scenes)[0]
	if want := (domain.Rect{X: 1220, Y: 420, W: 1000, H: 600}); s.WorkspaceClip != want || s.Windows[0].Rect != want {
		t.Fatalf("%+v %+v", s.WorkspaceClip, s.Windows[0])
	}
	// Larger than the monitor clamps to it: no clip, no invisible area.
	cfg.Workspaces = []domain.WorkspaceConfig{{Name: "focus", Size: [2]int{9000, 9000}}}
	r.reload <- domain.ConfigChanged{Config: cfg}
	s = receive(t, r.scenes)[0]
	if s.WorkspaceClip != (domain.Rect{}) || s.Windows[0].Rect != (domain.Rect{W: 3440, H: 1440}) {
		t.Fatalf("%+v %+v", s.WorkspaceClip, s.Windows[0])
	}
	cfg.Workspaces = []domain.WorkspaceConfig{{Name: "focus", Size: [2]int{1920, 1080}}}
	r.reload <- domain.ConfigChanged{Config: cfg}
	if s = receive(t, r.scenes)[0]; s.WorkspaceClip != frame {
		t.Fatal(s.WorkspaceClip)
	}
	cfg.Workspaces = []domain.WorkspaceConfig{{Name: "focus"}}
	r.reload <- domain.ConfigChanged{Config: cfg}
	if s = receive(t, r.scenes)[0]; s.WorkspaceClip != (domain.Rect{}) || s.Windows[0].Rect != (domain.Rect{W: 3440, H: 1440}) {
		t.Fatalf("%+v %+v", s.WorkspaceClip, s.Windows[0])
	}
}
func workspaceSizeFollowsOutputResize(t *testing.T, r *landRig) {
	r.mapLanded(t, 1)
	smaller := ultrawide
	smaller.Width, smaller.Height = 1600, 900
	s := r.land(r.plug(t, smaller))[0]
	// 1920x1080 no longer fits: it clamps to the monitor.
	if s.WorkspaceClip != (domain.Rect{}) || s.Windows[0].Rect != (domain.Rect{W: 1600, H: 900}) {
		t.Fatalf("%+v %+v", s.WorkspaceClip, s.Windows[0])
	}
	s = r.land(r.plug(t, ultrawide))[0]
	if s.WorkspaceClip != frame || s.Windows[0].Rect != frame {
		t.Fatalf("%+v %+v", s.WorkspaceClip, s.Windows[0])
	}
}
func workspaceSizeReservedZoneIntersectsFrame(t *testing.T, r *landRig) {
	// A bar inside the top of the monitor but above the frame reserves nothing of it.
	bar := domain.LayerSurface{ID: 9, Layer: domain.LayerTop, Anchor: ports.AnchorTop | ports.AnchorLeft | ports.AnchorRight, Height: 100, ExclusiveZone: 100}
	r.client <- ports.LayerChanged{Layers: []domain.LayerSurface{bar}}
	receive(t, r.scenes)
	s := r.mapLanded(t, 1)[0]
	if s.Windows[0].Rect != frame {
		t.Fatalf("%+v", s.Windows[0])
	}
	// A bar reaching into the frame shrinks it from the top.
	bar.Height, bar.ExclusiveZone = 300, 300
	r.client <- ports.LayerChanged{Layers: []domain.LayerSurface{bar}}
	s = receive(t, r.scenes)[0]
	if want := (domain.Rect{X: 760, Y: 300, W: 1920, H: 960}); s.Windows[0].Rect != want || s.WorkspaceClip != frame {
		t.Fatalf("%+v clip %+v", s.Windows[0], s.WorkspaceClip)
	}
}
