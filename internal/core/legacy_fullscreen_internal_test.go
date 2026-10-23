package core

import (
	"context"
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
			t.Fatal("setup: no second workspace")
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

// Only the focused window of the shown workspace is a candidate.
func TestLegacyFullscreenNeedsFocusAtFirstSize(t *testing.T) {
	t.Run("focus moved before the commit", func(t *testing.T) {
		c, _, _ := fadeCore(t)
		sc := c.cur()
		c.mapWindow(ports.WindowMapped{ID: 1})
		c.mapWindow(ports.WindowMapped{ID: 2})
		w := sc.mon.Current()
		w.FocusID(1)
		o := sc.rect()
		c.resizeFloating(ports.WindowResized{ID: 2, Width: o.W, Height: o.H})
		if w.fullscreen != 0 {
			t.Fatalf("unfocused window went fullscreen: %d", w.fullscreen)
		}
		if id, _ := w.Focused(); id != 1 {
			t.Fatalf("focus moved to %d", id)
		}
	})
	t.Run("quiet column", func(t *testing.T) {
		c, _, _ := fadeCore(t)
		sc := c.cur()
		c.mapWindow(ports.WindowMapped{ID: 1})
		c.windows.mapped(ports.WindowMapped{ID: 2}, c.now())
		w := sc.mon.Current()
		w.addColumnQuiet(Column{Windows: []WindowID{2}})
		o := sc.rect()
		c.resizeFloating(ports.WindowResized{ID: 2, Width: o.W, Height: o.H})
		if w.fullscreen != 0 {
			t.Fatalf("quiet window went fullscreen: %d", w.fullscreen)
		}
		if id, _ := w.Focused(); id != 1 {
			t.Fatalf("focus moved to %d", id)
		}
	})
	t.Run("quiet float", func(t *testing.T) {
		c, _, _ := fadeCore(t)
		sc := c.cur()
		c.mapWindow(ports.WindowMapped{ID: 1})
		c.windows.mapped(ports.WindowMapped{ID: 2, Floating: true}, c.now())
		w := sc.mon.Current()
		w.addFloat(Float{ID: 2}, true)
		o := sc.rect()
		c.resizeFloating(ports.WindowResized{ID: 2, Width: o.W, Height: o.H})
		if w.fullscreen != 0 {
			t.Fatalf("quiet float went fullscreen: %d", w.fullscreen)
		}
	})
}

// The adapter's sequence for a screen-sized tile mapping under a cover
// (bound fullscreen, animations on): the cover and its focus stay.
func TestLegacyFullscreenUnderBoundCover(t *testing.T) {
	for _, anim := range []bool{false, true} {
		c, _, _ := fadeCore(t)
		c.cfg.Animations.On = anim
		sc := c.cur()
		c.mapWindow(ports.WindowMapped{ID: 1})
		c.mapWindow(ports.WindowMapped{ID: 2})
		sc.mon.ToggleFullscreen()
		w := sc.mon.Current()
		if w.fullscreen != 2 {
			t.Fatalf("setup: fullscreen = %d", w.fullscreen)
		}
		c.clientEvent(context.Background(), ports.WindowMapped{ID: 3})
		o := sc.rect()
		c.clientEvent(context.Background(), ports.WindowResized{ID: 3, Width: o.W, Height: o.H})
		_ = c.publish(context.Background())
		if w.fullscreen != 2 {
			t.Fatalf("anim=%v: fullscreen = %d, want the cover 2", anim, w.fullscreen)
		}
		if id, _ := w.Focused(); id != 2 {
			t.Fatalf("anim=%v: focus moved to %d", anim, id)
		}
	}
}

func TestLegacyFullscreenCascadeFloatBand(t *testing.T) {
	for _, parent := range []WindowID{0, 1} {
		t.Run(map[bool]string{true: "dialog", false: "parentless"}[parent != 0], func(t *testing.T) {
			c, _, _ := fadeCore(t)
			sc := c.cur()
			w := sc.mon.Current()
			w.Overflow, w.MaxColumns = OverflowCascade, 3
			c.clientEvent(context.Background(), ports.WindowMapped{ID: 1})
			c.clientEvent(context.Background(), ports.WindowMapped{ID: 2, Floating: true, Parent: parent})
			o := sc.rect()
			c.clientEvent(context.Background(), ports.WindowResized{ID: 2, Width: o.W, Height: o.H})
			if parent != 0 {
				if !w.isFloat(2) || w.fullscreen != 0 {
					t.Fatal("dialog was promoted or auto-fullscreened")
				}
				c.clientEvent(context.Background(), ports.WindowFullscreenRequest{ID: 2, Fullscreen: true})
				if !w.isFloat(2) {
					t.Fatal("explicit fullscreen promoted a dialog")
				}
				return
			}
			if w.isFloat(2) || w.fullscreen != 2 || len(w.Columns) != 2 || w.band(w.columnOf(2)) != w.band(w.columnOf(1))+1 || !w.Columns[w.columnOf(2)].ownBand {
				t.Fatalf("fullscreen float placement: %+v, floats %+v fullscreen %d", w.Columns, w.Floats, w.fullscreen)
			}
			c.clientEvent(context.Background(), ports.WindowFullscreenRequest{ID: 2})
			if id, _ := w.Focused(); id != 2 || w.fullscreen != 0 || w.isFloat(2) || w.band(w.columnOf(2)) != 1 {
				t.Fatalf("after unset: focus %d fullscreen %d columns %+v floats %+v", id, w.fullscreen, w.Columns, w.Floats)
			}
			if r, ok := w.placed(2); !ok || r.W <= 0 || r.H <= 0 {
				t.Fatalf("game not visible: %+v %v", r, ok)
			}
		})
	}
}
