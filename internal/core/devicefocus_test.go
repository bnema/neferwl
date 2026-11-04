package core_test

import (
	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"testing"
	"testing/synctest"
)

func TestFocusFollowsInputDevice(t *testing.T) {
	for _, placed := range []bool{false, true} {
		t.Run(map[bool]string{false: "isolated", true: "placed"}[placed], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := startDragRig(t, func(c *domain.Config) {
					tv := domain.OutputConfig{Name: "tv", Virtual: true, Inputs: []string{"bound*"}}
					if placed {
						tv.Anchor = domain.OutputAnchor{Relation: domain.RelationRightOf, To: "DP-1"}
					}
					c.Outputs = []domain.OutputConfig{{Name: "DP-1"}, tv}
				}, domain.OutputInfo{Name: "DP-1", Width: 800, Height: 600}, domain.OutputInfo{Name: "tv", Virtual: true, Width: 640, Height: 360})
				r.in(ports.InputDeviceAdded{ID: 1, Name: "bound mouse"})
				r.in(ports.InputDeviceAdded{ID: 2, Name: "desktop mouse"})
				r.in(ports.InputDeviceAdded{ID: 3, Name: "bound tablet"})
				r.client <- ports.WindowMapped{ID: 1}
				r.settle(t)
				r.in(ports.KeyEvent{Device: 1, Keysym: "a", Keycode: 30, Pressed: true})
				r.settle(t)
				r.client <- ports.WindowMapped{ID: 2}
				r.settle(t)
				r.drain()
				for _, step := range []struct {
					device domain.InputDeviceID
					focus  domain.WindowID
				}{{2, 1}, {1, 2}, {3, 0}, {2, 1}, {3, 2}, {1, 0}, {2, 1}, {2, 0}} {
					r.in(ports.PointerAxis{Device: step.device})
					r.settle(t)
					got := focusChanges(r.drain())
					if step.focus == 0 {
						if len(got) != 0 {
							t.Fatalf("repeated device flapped: %v", got)
						}
					} else if len(got) != 1 || got[0] != step.focus {
						t.Fatalf("device %d focus %v want %d", step.device, got, step.focus)
					}
				}
			})
		})
	}
}

func TestSyncMotionPrecedesSameSurfaceClick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startDragRig(t, func(c *domain.Config) { c.Outputs = []domain.OutputConfig{{Name: "DP-1", Inputs: []string{"bound"}}} }, domain.OutputInfo{Name: "DP-1", Width: 800, Height: 600})
		r.client <- ports.WindowMapped{ID: 1}
		r.settle(t)
		r.in(ports.InputDeviceAdded{ID: 1, Name: "bound"})
		r.in(ports.InputDeviceAdded{ID: 2, Name: "other"})
		r.in(ports.PointerMotion{Device: 1, X: 100, Y: 100})
		r.settle(t)
		r.drain()
		r.in(ports.PointerMotion{Device: 2, X: 200, Y: 100})
		r.in(ports.PointerButton{Device: 2, Button: 272, Pressed: true})
		r.settle(t)
		cmds := r.drain()
		motion, button := -1, -1
		for i, cmd := range cmds {
			switch cmd.(type) {
			case ports.PointerMotionTo:
				motion = i
			case ports.PointerButtonTo:
				button = i
			}
		}
		if motion < 0 || button <= motion {
			t.Fatalf("motion must precede same-surface click: %#v", cmds)
		}
	})
}

func TestPlaceVirtualsSingleVirtualTakesFocus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newDragRigWith(t, false, core.Options{PlaceVirtuals: true}, func(c *domain.Config) {
			c.Outputs = []domain.OutputConfig{{Name: "tv", Virtual: true}}
		}, domain.OutputInfo{Name: "tv", Virtual: true, Width: 640, Height: 360})
		r.client <- ports.WindowMapped{ID: 1}
		r.settle(t)
		if got := focusChanges(r.drain()); len(got) == 0 || got[len(got)-1] != 1 {
			t.Fatalf("startup focus %v", got)
		}
		r.in(ports.InputDeviceAdded{ID: 1, Name: "mouse"})
		r.in(ports.PointerMotion{Device: 1, X: 100, Y: 100})
		r.settle(t)
		reached := false
		for _, cmd := range r.drain() {
			if m, ok := cmd.(ports.PointerMotionTo); ok {
				reached = m.ID == 1
			}
		}
		if !reached {
			t.Fatal("unbound pointer did not reach the virtual output")
		}
	})
}
