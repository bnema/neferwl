package core

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

// TestOSDKey drives owner events and timer delivery without wall-clock waits.
func TestOSDKey(t *testing.T) {
	type step struct {
		at     int
		action string
		want   osdKeyResult
	}
	tests := []struct {
		name  string
		pin   bool
		steps []step
	}{
		{"hold", false, []step{{0, "down", osdNone}, {400, "fire", osdShow}, {450, "up", osdHide}}},
		{"early release", false, []step{{0, "down", osdNone}, {399, "up", osdNone}, {400, "stale", osdNone}}},
		{"other during hold", false, []step{{0, "down", osdNone}, {100, "otherDown", osdNone}, {400, "stale", osdNone}, {450, "up", osdNone}}},
		{"double", false, []step{{0, "down", osdNone}, {50, "up", osdNone}, {350, "down", osdNone}, {400, "up", osdTogglePin}}},
		{"slow", false, []step{{0, "down", osdNone}, {50, "up", osdNone}, {351, "down", osdNone}, {400, "up", osdNone}}},
		{"long then tap", false, []step{{0, "down", osdNone}, {300, "up", osdNone}, {350, "down", osdNone}, {400, "up", osdNone}}},
		{"bind between taps", false, []step{{0, "down", osdNone}, {50, "up", osdNone}, {100, "down", osdNone}, {120, "otherDown", osdNone}, {140, "otherUp", osdNone}, {150, "up", osdNone}, {200, "down", osdNone}, {250, "up", osdNone}}},
		{"triple", false, []step{{0, "down", osdNone}, {50, "up", osdNone}, {100, "down", osdNone}, {150, "up", osdTogglePin}, {200, "down", osdNone}, {250, "up", osdNone}}},
		{"pinned hold", true, []step{{0, "down", osdNone}, {400, "stale", osdNone}, {450, "up", osdNone}}},
		{"pinned double", true, []step{{0, "down", osdNone}, {50, "up", osdNone}, {100, "down", osdNone}, {150, "up", osdTogglePin}}},
		{"already held", false, []step{{0, "otherDown", osdNone}, {10, "down", osdNone}, {400, "stale", osdNone}, {450, "up", osdNone}}},
		{"repeat press", false, []step{{0, "down", osdNone}, {100, "down", osdNone}, {400, "fire", osdShow}, {450, "up", osdHide}}},
		{"tap limit", false, []step{{0, "down", osdNone}, {250, "up", osdNone}, {300, "down", osdNone}, {550, "up", osdTogglePin}}},
	}
	for _, cmd := range []struct {
		sym string
		mod domain.Mods
	}{{"Super_L", domain.ModSuper}, {"Super_R", domain.ModSuper}, {"Alt_L", domain.ModAlt}, {"Alt_R", domain.ModAlt}, {"Control_L", domain.ModCtrl}, {"Control_R", domain.ModCtrl}} {
		for _, tt := range tests {
			t.Run(cmd.sym+"/"+tt.name, func(t *testing.T) {
				clock := portsmocks.NewMockClock(t)
				var fire chan time.Time
				clock.EXPECT().NewTimer(osdHoldDelay).RunAndReturn(func(time.Duration) ports.Timer {
					timer := portsmocks.NewMockTimer(t)
					ch := make(chan time.Time, 1)
					fire = ch
					timer.EXPECT().C().Return(ch).Once()
					timer.EXPECT().Stop().Return(true).Once()
					return timer
				}).Maybe()
				k := osdKey{clock: clock}
				k.setPinned(tt.pin)
				if k.pinned() != tt.pin {
					t.Fatal("pin state")
				}
				for _, s := range tt.steps {
					now := time.Unix(0, 0).Add(time.Duration(s.at) * time.Millisecond)
					var got osdKeyResult
					switch s.action {
					case "fire":
						if k.holdC == nil {
							t.Fatal("hold not armed")
						}
						// Deliver through the port channel, then dispatch on the owner.
						fire <- now
						<-k.holdC
						got = k.holdFired()
					case "stale":
						if k.holdC != nil {
							t.Fatal("cancelled hold still armed")
						}
						got = k.holdFired()
					default:
						ev := ports.KeyEvent{Keysym: cmd.sym, Keycode: 125, Pressed: s.action == "down"}
						if ev.Pressed {
							ev.Mods = cmd.mod
						}
						if s.action == "otherDown" || s.action == "otherUp" {
							ev.Keysym = "a"
							ev.Keycode = 30
							ev.Pressed = s.action == "otherDown"
							ev.Mods = cmd.mod
						}
						got = k.key(ev, heldKey(ev), cmd.mod, now)
					}
					if got != s.want {
						t.Fatalf("%d %s: got %v, want %v", s.at, s.action, got, s.want)
					}
				}
				k.stopHold()
			})
		}
	}
}
