package core

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

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

// hoverEventCore reuses the indicator rig's generated clock and command sink.
func hoverEventCore(t *testing.T, n int) (*Core, *indicatorClock, chan ports.ClientCommand) {
	t.Helper()
	cmds := make(chan ports.ClientCommand, 256)
	c, clock := indicatorCoreWith(t, cmds)
	c.cfg.Focus.FollowMouse = true
	c.cfg.Layout.Overflow = "scroll"
	c.settings(c.cur().mon)
	for id := WindowID(1); id <= WindowID(n); id++ {
		c.windows.mapped(ports.WindowMapped{ID: id}, clock.now)
		c.cur().mon.AddWindow(id)
	}
	indicatorScene(t, c)
	drainHoverFocus(cmds)
	return c, clock, cmds
}

func hoverMove(t *testing.T, c *Core, x, y float64) {
	t.Helper()
	if err := c.pointerMotion(context.Background(), ports.PointerMotion{X: x, Y: y}); err != nil {
		t.Fatal(err)
	}
}

func hoverBind(t *testing.T, c *Core, a Action) {
	t.Helper()
	if err := c.runBind(context.Background(), a); err != nil {
		t.Fatal(err)
	}
}

func hoverWantFocus(t *testing.T, c *Core, id WindowID) {
	t.Helper()
	if got, _ := c.cur().mon.Focused(); got != id {
		t.Fatalf("focus %d want %d", got, id)
	}
}

func hoverNoFocusCommand(t *testing.T, cmds <-chan ports.ClientCommand) {
	t.Helper()
	if ids := drainHoverFocus(cmds); len(ids) != 0 {
		t.Fatalf("unexpected focus commands %v", ids)
	}
}

func TestHoverAlreadyFocusedFromGap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _, cmds := hoverEventCore(t, 2)
		c.cur().mon.Current().Gaps = 12
		indicatorScene(t, c)
		hoverMove(t, c, 1, 1)
		if c.pointer != 0 {
			t.Fatal("setup: expected a gap")
		}
		drainHoverFocus(cmds)
		for len(c.ch.Scenes) > 0 {
			<-c.ch.Scenes
		}
		x, y := hoverPoint(t, c, 2)
		hoverMove(t, c, x, y)
		if c.pointer != 2 {
			t.Fatal("did not enter focused window")
		}
		hoverWantFocus(t, c, 2)
		hoverNoFocusCommand(t, cmds)
		if len(c.ch.Scenes) != 0 {
			t.Fatal("already-focused entry published")
		}
	})
}

func TestHoverLayoutSlidesUnderCursor(t *testing.T) {
	for _, animated := range []bool{false, true} {
		t.Run(map[bool]string{false: "immediate", true: "animated"}[animated], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, clock, cmds := hoverEventCore(t, 4)
				for range 3 {
					hoverBind(t, c, ActionFocusColumnLeft)
				}
				c.cfg.Animations.On = animated
				hoverMove(t, c, 40, 100)
				if c.pointer != 1 {
					t.Fatalf("setup pointer %d", c.pointer)
				}
				for range 3 {
					hoverBind(t, c, ActionFocusColumnRight)
				}
				hoverWantFocus(t, c, 4)
				if animated {
					// The same generated-clock/frame rig used by scene animation tests.
					clock.now = clock.now.Add(16 * time.Millisecond)
					if s := stepScenes(t, c, c.cur())["A"]; !s.Animating {
						t.Fatal("scroll did not animate")
					}
					clock.now = clock.now.Add(5 * time.Second)
					if s := stepScenes(t, c, c.cur())["A"]; s.Animating {
						t.Fatal("scroll did not settle")
					}
				}
				id, _, _ := c.hit(40, 100)
				if id == 0 || id == 1 || id == 4 {
					t.Fatalf("setup: expected another column under cursor, got %d", id)
				}
				drainHoverFocus(cmds)
				hoverMove(t, c, 41, 100)
				hoverWantFocus(t, c, 4)
				hoverNoFocusCommand(t, cmds)
			})
		})
	}
}

func TestHoverRevealDoesNotCascade(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _, cmds := hoverEventCore(t, 3)
		w := c.cur().mon.Current()
		w.Columns[1].Cells = 2
		hoverBind(t, c, ActionFocusColumnLeft)
		hoverBind(t, c, ActionFocusColumnLeft)
		hoverMove(t, c, 50, 100)
		if c.pointer != 1 {
			t.Fatal("setup: pointer not on first column")
		}
		var target Rect
		for _, p := range c.cur().mon.Layout() {
			if p.ID == 2 {
				target = p.Rect
			}
		}
		if target.X >= 300 || target.X+target.W <= 300 {
			t.Fatalf("column not partly offscreen: %+v", target)
		}
		before := w.View
		drainHoverFocus(cmds)
		hoverMove(t, c, 280, 100)
		hoverWantFocus(t, c, 2)
		if ids := drainHoverFocus(cmds); len(ids) != 1 || ids[0] != 2 {
			t.Fatalf("hover focus commands %v", ids)
		}
		if w.View == before {
			t.Fatal("hover did not scroll")
		}
		hoverMove(t, c, 281, 100)
		hoverWantFocus(t, c, 2)
		hoverNoFocusCommand(t, cmds)
	})
}

func TestHoverUnmapUnderCursor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _, cmds := hoverEventCore(t, 3)
		hoverBind(t, c, ActionFocusColumnLeft)
		hoverBind(t, c, ActionFocusColumnLeft)
		hoverMove(t, c, 160, 100)
		// The hover selected the window being removed; unmap must select a
		// different fallback without letting jitter select its replacement.
		id := c.pointer
		if id == 0 {
			t.Fatal("setup: no window under cursor")
		}
		hoverWantFocus(t, c, id)
		drainHoverFocus(cmds)
		publish, stop := c.clientEvent(context.Background(), ports.WindowUnmapped{ID: id})
		if stop {
			t.Fatal("unmap stopped core")
		}
		if publish {
			indicatorScene(t, c)
		}
		fallback, _ := c.cur().mon.Focused()
		under, _, _ := c.hit(160, 100)
		if fallback == 0 || fallback == id || under == 0 || under == fallback {
			t.Fatalf("setup: removed=%d fallback=%d under=%d", id, fallback, under)
		}
		if ids := drainHoverFocus(cmds); len(ids) != 1 || ids[0] != fallback {
			t.Fatalf("fallback focus commands %v, want %d", ids, fallback)
		}
		hoverMove(t, c, 161, 100)
		hoverWantFocus(t, c, fallback)
		hoverNoFocusCommand(t, cmds)
	})
}

func TestHoverInputGuards(t *testing.T) {
	for _, guard := range []string{"held button", "overview"} {
		t.Run(guard, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, _, cmds := hoverEventCore(t, 2)
				x, y := hoverPoint(t, c, 2)
				hoverMove(t, c, x, y)
				if guard == "held button" {
					if err := c.pointerButton(context.Background(), ports.PointerButton{Button: btnLeft, Pressed: true}); err != nil {
						t.Fatal(err)
					}
					if !c.buttons[btnLeft] {
						t.Fatal("button not held")
					}
				} else {
					hoverBind(t, c, ActionToggleOverview)
					if !c.cur().mon.previewing() {
						t.Fatal("overview not previewing")
					}
				}
				x, y = hoverPoint(t, c, 1)
				if guard == "overview" {
					// Overview cards use the published preview geometry, not the
					// workspace's ordinary tile rectangles.
					s := indicatorScene(t, c)
					for _, p := range s.Windows {
						if p.ID == 1 {
							x, y = float64(p.Rect.X+p.Rect.W/2), float64(p.Rect.Y+p.Rect.H/2)
						}
					}
				}
				drainHoverFocus(cmds)
				hoverMove(t, c, x, y)
				wantPointer := WindowID(1)
				if guard == "overview" {
					// Preview cards intentionally take no client pointer input.
					wantPointer = 0
				}
				if c.pointer != wantPointer {
					t.Fatalf("guard motion pointer %d, want %d", c.pointer, wantPointer)
				}
				hoverWantFocus(t, c, 2)
				hoverNoFocusCommand(t, cmds)
			})
		})
	}
}

func TestHoverShownStash(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _, _, cmds := stashRig(t, false)
		c.cfg.Focus.FollowMouse = true
		indicatorScene(t, c)
		w := c.cur().mon.Current()
		if !w.StashShown() {
			t.Fatal("setup: stash hidden")
		}
		// Find exposed tile and peek pixels through the real hit tester: the
		// selected stash window overlaps their layout rectangles.
		point := func(id WindowID) (float64, float64) {
			t.Helper()
			for y := 5; y < 200; y += 5 {
				for x := 5; x < 300; x += 5 {
					if hit, _, _ := c.hit(float64(x), float64(y)); hit == id {
						return float64(x), float64(y)
					}
				}
			}
			t.Fatalf("no exposed pixels for %d", id)
			return 0, 0
		}
		x, y := point(2)
		hoverMove(t, c, x, y)
		drainHoverFocus(cmds)
		x, y = point(1)
		hoverMove(t, c, x, y)
		hoverWantFocus(t, c, 2)
		if !w.StashShown() {
			t.Fatal("tile hover hid stash")
		}
		hoverNoFocusCommand(t, cmds)
		x, y = point(3)
		hoverMove(t, c, x, y)
		hoverWantFocus(t, c, 3)
		if !w.StashShown() {
			t.Fatal("peek hover hid stash")
		}
		if ids := drainHoverFocus(cmds); len(ids) != 1 || ids[0] != 3 {
			t.Fatalf("peek focus commands %v", ids)
		}
	})
}
