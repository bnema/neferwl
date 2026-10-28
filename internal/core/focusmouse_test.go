package core

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func hoverPoint(t *testing.T, c *Core, id WindowID) (float64, float64) {
	t.Helper()
	for _, p := range c.cur().mon.Layout() {
		if p.ID == id {
			return float64(p.Rect.X + p.Rect.W/2), float64(p.Rect.Y + p.Rect.H/2)
		}
	}
	t.Fatalf("window %d not laid out", id)
	return 0, 0
}

func drainHoverFocus(commands <-chan ports.ClientCommand) []WindowID {
	var ids []WindowID
	for {
		select {
		case cmd := <-commands:
			if f, ok := cmd.(ports.FocusWindow); ok {
				ids = append(ids, f.ID)
			}
		default:
			return ids
		}
	}
}

func TestHoverFocusMotion(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[on], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				commands := make(chan ports.ClientCommand, 64)
				c, clock := indicatorCoreWith(t, commands)
				for id := WindowID(1); id <= 3; id++ {
					c.windows.mapped(ports.WindowMapped{ID: id}, clock.now)
					c.cur().mon.AddWindow(id)
				}
				c.cfg.Animations.On = false
				c.cfg.Focus.FollowMouse = on
				indicatorScene(t, c)
				x, y := hoverPoint(t, c, 3)
				if err := c.pointerMotion(context.Background(), ports.PointerMotion{X: x, Y: y}); err != nil {
					t.Fatal(err)
				}
				drainHoverFocus(commands)
				x, y = hoverPoint(t, c, 2)
				if err := c.pointerMotion(context.Background(), ports.PointerMotion{X: x, Y: y}); err != nil {
					t.Fatal(err)
				}
				want := WindowID(3)
				if on {
					want = 2
				}
				if f, _ := c.cur().mon.Focused(); f != want {
					t.Fatalf("focus %d want %d", f, want)
				}
				ids := drainHoverFocus(commands)
				if on && (len(ids) != 1 || ids[0] != 2) {
					t.Fatalf("focus commands %v", ids)
				}
				if !on && len(ids) != 0 {
					t.Fatalf("disabled focus commands %v", ids)
				}
				if c.flash.pending != nil {
					t.Fatal("hover flashed")
				}
				// Same-window motion must not publish or send keyboard focus again.
				for len(c.ch.Scenes) > 0 {
					<-c.ch.Scenes
				}
				if err := c.pointerMotion(context.Background(), ports.PointerMotion{X: x + 1, Y: y}); err != nil {
					t.Fatal(err)
				}
				if ids := drainHoverFocus(commands); len(ids) != 0 {
					t.Fatalf("repeat focus %v", ids)
				}
				if len(c.ch.Scenes) != 0 {
					t.Fatal("same window published")
				}
			})
		})
	}
}

func TestHoverEntryGuardsAndHold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := flashCore(t)
		c.cfg.Animations.On = false
		c.cfg.Focus.FollowMouse = true
		indicatorScene(t, c)
		x, y := hoverPoint(t, c, 3)
		for _, guard := range []string{"button", "grab", "synthetic", "overview"} {
			c.hoverLocked, c.hoverLock = true, 3
			switch guard {
			case "button":
				c.buttons[btnLeft] = true
			case "grab":
				c.grab = 3
			case "synthetic":
				c.synthetic = true
			case "overview":
				c.cur().mon.ToggleOverview()
			}
			if c.hoverEntry(2, x, y) {
				t.Fatalf("%s admitted entry", guard)
			}
			if !c.hoverLocked || c.hoverLock != 3 {
				t.Fatalf("%s changed hold", guard)
			}
			delete(c.buttons, btnLeft)
			c.grab = 0
			c.synthetic = false
			if guard == "overview" {
				c.cur().mon.ToggleOverview()
			}
		}
		c.hoverLocked, c.hoverLock = true, 2
		if c.hoverEntry(2, x, y) {
			t.Fatal("held window admitted")
		}
		c.hoverLock = 3
		if !c.hoverEntry(2, x, y) || c.hoverLocked {
			t.Fatal("new entry did not release hold")
		}
		x, y = hoverPoint(t, c, 2)
		c.hoverLocked, c.hoverLock = true, 3
		if c.hoverEntry(2, x, y) || c.hoverLock != 2 {
			t.Fatal("slid-in window admitted or hold not retargeted")
		}
	})
}

func TestHoverKeyboardHoldMotion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock := indicatorCore(t)
		c.cfg.Layout.MaxColumns = 3
		c.settings(c.cur().mon)
		for id := WindowID(1); id <= 3; id++ {
			c.windows.mapped(ports.WindowMapped{ID: id}, clock.now)
			c.cur().mon.AddWindow(id)
		}
		c.cfg.Animations.On = false
		c.cfg.Focus.FollowMouse = true
		indicatorScene(t, c)
		x, y := hoverPoint(t, c, 3)
		ctx := context.Background()
		move := func(x, y float64) {
			t.Helper()
			if err := c.pointerMotion(ctx, ports.PointerMotion{X: x, Y: y}); err != nil {
				t.Fatal(err)
			}
		}
		move(x, y)
		c.applyAction(ActionFocusColumnLeft)
		indicatorScene(t, c)
		if f, _ := c.cur().mon.Focused(); f != 2 {
			t.Fatalf("action focus %d", f)
		}
		move(x+1, y)
		move(x, -1)
		move(x, y)
		if f, _ := c.cur().mon.Focused(); f != 2 {
			t.Fatalf("hold lost: %d", f)
		}
		x, y = hoverPoint(t, c, 1)
		move(x, y)
		if f, _ := c.cur().mon.Focused(); f != 1 {
			t.Fatalf("fresh entry focus %d", f)
		}
	})
}

func TestHoverOtherOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock := flashCore(t)
		c.cfg.Animations.On = false
		c.cfg.Focus.FollowMouse = true
		c.addScreen(domain.OutputInfo{Name: "B", Width: 300, Height: 200})
		b := c.screens[1]
		c.windows.mapped(ports.WindowMapped{ID: 4}, clock.now)
		c.windows.mapped(ports.WindowMapped{ID: 5}, clock.now)
		b.mon.AddWindow(4)
		b.mon.AddWindow(5)
		indicatorScene(t, c)
		ctx := context.Background()
		x, y := hoverPoint(t, c, 3)
		if err := c.pointerMotion(ctx, ports.PointerMotion{X: x, Y: y}); err != nil {
			t.Fatal(err)
		}
		var target Rect
		for _, p := range b.mon.Layout() {
			if p.ID == 4 {
				target = p.Rect
			}
		}
		x, y = float64(b.x+target.X+target.W/2), float64(b.y+target.Y+target.H/2)
		if err := c.pointerMotion(ctx, ports.PointerMotion{X: x, Y: y}); err != nil {
			t.Fatal(err)
		}
		if c.cur() != b {
			t.Fatal("output did not follow pointer")
		}
		if f, _ := b.mon.Focused(); f != 4 {
			t.Fatalf("other output focus %d", f)
		}
	})
}
