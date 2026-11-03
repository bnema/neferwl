package core_test

import (
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
