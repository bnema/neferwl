package core

import (
	"context"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestVirtualPointerUnavailableOutput(t *testing.T) {
	for _, isolated := range []bool{true, false} {
		name := "removed"
		if isolated {
			name = "isolated"
		}
		t.Run(name, func(t *testing.T) {
			c := layoutCore(0, tile{"desktop", 0, 0, 100, 80}, tile{"tv", 100, 0, 100, 80})
			c.opts = testOptions(t, c.opts)
			c.activity = c.now()
			commands := make(chan ports.ClientCommand, 64)
			c.ch.Commands = commands
			c.buttons = make(map[uint32]bool)
			c.swallow = make(map[uint32]bool)
			c.cursorX, c.cursorY, c.pointer = 25, 30, 1
			send := func(event ports.InputEvent, absolute bool) {
				t.Helper()
				if err := c.virtualPointer(context.Background(), ports.VirtualPointerInput{Output: "tv", Absolute: absolute, FX: .5, FY: .5, Event: event}); err != nil {
					t.Fatal(err)
				}
			}
			// A press accepted before the target becomes unavailable must be released.
			send(ports.PointerButton{Button: 0x110, Pressed: true}, false)
			if !c.buttons[0x110] {
				t.Fatal("valid bound press dropped")
			}
			<-commands
			if isolated {
				c.screens[1].isolated = true
			} else {
				c.screens = c.screens[:1]
			}
			if r, ok := c.virtualArea("tv"); ok || r != (domain.Rect{}) {
				t.Fatalf("unavailable area: %+v %v", r, ok)
			}
			send(ports.PointerMotion{DX: 50, DY: 50}, true)
			send(ports.PointerMotion{DX: 50, DY: 50}, false)
			send(ports.PointerButton{Button: 0x111, Pressed: true}, false)
			send(ports.PointerButton{Button: 0x111}, false)
			send(ports.PointerAxis{Vertical: domain.ScrollAxis{Set: true, Value: 15}}, false)
			if c.cursorX != 25 || c.cursorY != 30 || c.pointer != 1 || c.focusScreen != 0 || len(commands) != 0 || c.buttons[0x111] {
				t.Fatalf("unavailable input changed desktop: cursor %v,%v pointer %v focus %v commands %v", c.cursorX, c.cursorY, c.pointer, c.focusScreen, len(commands))
			}
			send(ports.PointerButton{Button: 0x110}, false)
			if c.held() != 0 || c.grab != 0 {
				t.Fatal("stuck held state")
			}
			release, ok := (<-commands).(ports.PointerButtonTo)
			if !ok || release.Pressed || release.ID != 1 {
				t.Fatalf("release: %+v", release)
			}
		})
	}
}
