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
