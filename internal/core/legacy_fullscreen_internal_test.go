package core

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// A window whose first committed size is its output's size goes fullscreen,
// focused, exactly as toggle-fullscreen does, and toggling leaves it.
func TestLegacyFullscreenAtOutputSize(t *testing.T) {
	c, _, _ := fadeCore(t)
	sc := c.cur()
	c.mapWindow(ports.WindowMapped{ID: 1})
	c.mapWindow(ports.WindowMapped{ID: 2})
	o := sc.rect()
	c.resizeFloating(ports.WindowResized{ID: 2, Width: o.W, Height: o.H})
	w := sc.mon.Current()
	if w.fullscreen != 2 {
		t.Fatalf("fullscreen = %d, want 2", w.fullscreen)
	}
	if id, _ := w.Focused(); id != 2 {
		t.Fatalf("focused %d, want 2", id)
	}
	sc.mon.ToggleFullscreen()
	if w.fullscreen != 0 {
		t.Fatalf("toggle left fullscreen = %d", w.fullscreen)
	}
}

func TestLegacyFullscreenFloatingFixedSize(t *testing.T) {
	c, _, _ := fadeCore(t)
	sc := c.cur()
	c.mapWindow(ports.WindowMapped{ID: 1, Floating: true})
	o := sc.rect()
	c.resizeFloating(ports.WindowResized{ID: 1, Width: o.W, Height: o.H})
	if sc.mon.Current().fullscreen != 1 {
		t.Fatal("a screen-sized float did not go fullscreen")
	}
}

func TestLegacyFullscreenNoFalsePositive(t *testing.T) {
	t.Run("smaller", func(t *testing.T) {
		c, _, _ := fadeCore(t)
		sc := c.cur()
		c.mapWindow(ports.WindowMapped{ID: 1})
		o := sc.rect()
		c.resizeFloating(ports.WindowResized{ID: 1, Width: o.W - 1, Height: o.H})
		if sc.mon.Current().fullscreen != 0 {
			t.Fatal("smaller window went fullscreen")
		}
	})
	t.Run("parent", func(t *testing.T) {
		c, _, _ := fadeCore(t)
		sc := c.cur()
		c.mapWindow(ports.WindowMapped{ID: 1})
		c.mapWindow(ports.WindowMapped{ID: 2, Floating: true, Parent: 1})
		o := sc.rect()
		c.resizeFloating(ports.WindowResized{ID: 2, Width: o.W, Height: o.H})
		if sc.mon.Current().fullscreen != 0 {
			t.Fatal("dialog went fullscreen")
		}
	})
	t.Run("later resize", func(t *testing.T) {
		c, _, _ := fadeCore(t)
		sc := c.cur()
		c.mapWindow(ports.WindowMapped{ID: 1})
		o := sc.rect()
		c.resizeFloating(ports.WindowResized{ID: 1, Width: 50, Height: 50})
		c.resizeFloating(ports.WindowResized{ID: 1, Width: o.W, Height: o.H})
		if sc.mon.Current().fullscreen != 0 {
			t.Fatal("a later resize went fullscreen")
		}
	})
}

// With animations on, the entrance never restarts for the window that went
// fullscreen: no motion, and the scene shows it at the full output.
func TestLegacyFullscreenDropsEntrance(t *testing.T) {
	c, _, _ := fadeCore(t)
	c.cfg.Animations.On = true
	sc := c.cur()
	c.mapWindow(ports.WindowMapped{ID: 1})
	c.stopAnimations()
	c.mapWindow(ports.WindowMapped{ID: 2})
	o := sc.rect()
	c.resizeFloating(ports.WindowResized{ID: 2, Width: o.W, Height: o.H})
	s := indicatorScene(t, c)
	if _, ok := sc.rects[2]; ok {
		t.Fatalf("entrance motion left for the fullscreen window: %+v", sc.rects)
	}
	if w := sceneWindow(t, s, 2); w.Rect != (Rect{W: o.W, H: o.H}) || w.Fade != 0 {
		t.Fatalf("scene window %+v, want the full output %+v", w, o)
	}
}

// Focus rules (ADR 011): never takes the focus from a fullscreen cover, nor
// acts on a workspace that is not shown.
func TestLegacyFullscreenRespectsFocus(t *testing.T) {
	t.Run("under a fullscreen window", func(t *testing.T) {
		c, _, _ := fadeCore(t)
		sc := c.cur()
		c.mapWindow(ports.WindowMapped{ID: 1})
		sc.mon.SetFullscreen(1, true)
		c.mapWindow(ports.WindowMapped{ID: 2})
		o := sc.rect()
		c.resizeFloating(ports.WindowResized{ID: 2, Width: o.W, Height: o.H})
		w := sc.mon.Current()
		if w.fullscreen != 1 {
			t.Fatalf("fullscreen = %d, want the cover 1", w.fullscreen)
		}
		if id, _ := w.Focused(); id != 1 {
			t.Fatalf("focus moved to %d", id)
		}
	})
	t.Run("other workspace", func(t *testing.T) {
		c, _, _ := fadeCore(t)
		sc := c.cur()
		c.mapWindow(ports.WindowMapped{ID: 1})
		ws := sc.mon.Current()
		sc.mon.Focus(1)
		if sc.mon.Current() == ws {
			t.Skip("no second workspace")
		}
		o := sc.rect()
		c.resizeFloating(ports.WindowResized{ID: 1, Width: o.W, Height: o.H})
		if ws.fullscreen != 0 {
			t.Fatal("a window on a hidden workspace went fullscreen")
		}
	})
}

func TestLegacyFullscreenRemapAndOutputs(t *testing.T) {
	t.Run("remap", func(t *testing.T) {
		c, _, _ := fadeCore(t)
		sc := c.cur()
		c.mapWindow(ports.WindowMapped{ID: 1, Remap: true})
		o := sc.rect()
		c.resizeFloating(ports.WindowResized{ID: 1, Width: o.W, Height: o.H})
		if sc.mon.Current().fullscreen != 0 {
			t.Fatal("a remap went fullscreen")
		}
	})
	second := func(t *testing.T) (*Core, *screen) {
		c, _, _ := fadeCore(t)
		c.addScreen(ports.OutputInfo{Name: "B", Width: 500, Height: 300})
		c.focusScreen = c.screenIndex("B")
		return c, c.cur()
	}
	t.Run("sized to its own output", func(t *testing.T) {
		c, b := second(t)
		c.mapWindow(ports.WindowMapped{ID: 1})
		if s, _ := c.screenOf(1); s != b {
			t.Fatal("window not on B")
		}
		c.resizeFloating(ports.WindowResized{ID: 1, Width: 500, Height: 300})
		if b.mon.Current().fullscreen != 1 {
			t.Fatal("window sized to its output B did not go fullscreen")
		}
	})
	t.Run("sized to another output", func(t *testing.T) {
		c, b := second(t)
		c.mapWindow(ports.WindowMapped{ID: 1})
		c.resizeFloating(ports.WindowResized{ID: 1, Width: 300, Height: 200})
		if b.mon.Current().fullscreen != 0 {
			t.Fatal("window sized to output A went fullscreen on B")
		}
	})
}
