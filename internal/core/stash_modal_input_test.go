package core

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestStashBlocksTilePointerInput(t *testing.T) {
	for _, cover := range []bool{false, true} {
		name := "tile"
		if cover {
			name = "fullscreen"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, _, sc, cmds := stashRig(t, false)
				w := sc.mon.Current()
				if cover {
					w.ToggleStashVisible()
					w.FocusID(1)
					sc.mon.ToggleFullscreen()
					w.ToggleStashVisible()
					settleShown(t, c)
				}
				// Find exposed tile area, above the stash strip.
				r := c.clientRect(settledOf(t, sc, 1))
				x, y := float64(r.X+r.W/2), float64(r.Y+1)
				if id, _, _ := c.hit(x, y); id != 0 {
					t.Fatalf("blocked hit %d", id)
				}
				send := func(ev ports.InputEvent) {
					t.Helper()
					if err := c.handleInput(t.Context(), ev); err != nil {
						t.Fatal(err)
					}
				}
				drainConfigures(cmds)
				// Stale focus must not leak axis input when a bind shows the stash.
				c.pointer = 1
				send(ports.PointerAxis{Vertical: domain.ScrollAxis{Set: true, Value: 10}})
				send(ports.PointerMotion{X: x, Y: y})
				send(ports.PointerAxis{Vertical: domain.ScrollAxis{Set: true, Value: 10}})
				for len(cmds) > 0 {
					switch v := (<-cmds).(type) {
					case ports.PointerFocus:
						if v.ID == 1 {
							t.Fatal("tile received pointer enter behind stash")
						}
					case ports.PointerMotionTo:
						if v.ID == 1 {
							t.Fatal("tile received motion behind stash")
						}
					case ports.PointerAxisTo:
						if v.ID == 1 {
							t.Fatal("tile received axis behind stash")
						}
					}
				}
				send(ports.PointerButton{Button: btnLeft, Pressed: true})
				if w.StashShown() {
					t.Fatal("click did not hide stash")
				}
				if w.Focus != 0 || w.Columns[0].Focus != 0 {
					t.Fatal("click changed column selection")
				}
				// Motion before release must not expose the swallowed release.
				send(ports.PointerMotion{X: x, Y: y})
				send(ports.PointerButton{Button: btnLeft})
				buttons := 0
				for len(cmds) > 0 {
					switch v := (<-cmds).(type) {
					case ports.PointerButtonTo:
						t.Fatalf("dismissal leaked button %+v", v)
					case ports.PointerAxisTo:
						if v.ID == 1 {
							t.Fatal("axis leaked to tile")
						}
					case ports.PointerMotionTo:
						if v.ID == 1 {
							buttons++
						}
					}
				}
				if buttons == 0 {
					t.Fatal("tile did not receive motion after hide")
				}
				send(ports.PointerButton{Button: btnLeft, Pressed: true})
				send(ports.PointerButton{Button: btnLeft})
				buttons = 0
				for len(cmds) > 0 {
					if v, ok := (<-cmds).(ports.PointerButtonTo); ok && v.ID == 1 {
						buttons++
					}
				}
				if buttons != 2 {
					t.Fatalf("after hide got %d button events", buttons)
				}
			})
		})
	}
}

func TestStashInteractiveWindows(t *testing.T) {
	for _, target := range []WindowID{2, 3, 9} {
		t.Run(string(rune('0'+target)), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, _, sc, cmds := stashRig(t, false)
				if target == 9 {
					sc.mon.Current().AddFloating(9, 100, 80)
					settleShown(t, c)
				}
				p := settledOf(t, sc, target)
				r := intersect(p.Rect, sc.mon.viewport().area(p))
				x, y := float64(r.X+r.W/2), float64(r.Y+r.H/2)
				if target == 3 {
					x = float64(r.X + r.W - 1)
				}
				if err := c.pointerMotion(t.Context(), ports.PointerMotion{X: x, Y: y}); err != nil {
					t.Fatal(err)
				}
				if c.pointer != target {
					t.Fatalf("hit %d want %d at %v,%v", c.pointer, target, x, y)
				}
				drainConfigures(cmds)
				for _, pressed := range []bool{true, false} {
					if err := c.pointerButton(t.Context(), ports.PointerButton{Button: btnLeft, Pressed: pressed}); err != nil {
						t.Fatal(err)
					}
				}
				n := 0
				for len(cmds) > 0 {
					if v, ok := (<-cmds).(ports.PointerButtonTo); ok && v.ID == target {
						n++
					}
				}
				if n != 2 {
					t.Fatalf("got %d buttons for %d", n, target)
				}
			})
		})
	}
}
