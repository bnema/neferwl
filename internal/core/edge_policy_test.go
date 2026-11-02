package core

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestScreenEdgePolicyStaysWithoutEventOrFlash(t *testing.T) {
	for _, overflow := range []Overflow{OverflowScroll, OverflowCascade} {
		t.Run(string(overflow), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, clock := flashWorkspaces(t, overflow)
				m := c.cur().mon
				m.Focus(0)
				action := ActionFocusWindowDown
				if overflow == OverflowCascade {
					action = ActionFocusColumnRight
				}
				traces := make(chan domain.InputTrace, 4)
				c.ch.Binds = traces
				before := m.Current()
				c.applyAction(action)
				if m.Current() != before || m.Active != 0 {
					t.Fatal("default edge changed workspace")
				}
				if len(traces) != 0 {
					t.Fatal("default edge emitted a bind event")
				}
				if c.flash.pending != nil || flashTimers(clock) != 0 {
					t.Fatal("default edge requested a flash")
				}
			})
		})
	}
}

func TestClampedWorkspaceEdgeDoesNotMove(t *testing.T) {
	for _, policy := range []domain.NavEdge{domain.NavEdgeScreen, domain.NavEdgeWorkspace} {
		c := layoutCore(0, tile{"M", 0, 0, 100, 80})
		c.cfg.Navigation.Edge = policy
		traces := make(chan domain.InputTrace, 4)
		c.ch.Binds = traces
		m := c.cur().mon
		m.switchList = append([]*Workspace(nil), m.Workspaces...)
		if c.edgeFocus(layout.Vertical, -1, dirUp) {
			t.Fatal("clamped edge reported a move")
		}
		if m.switchList == nil {
			t.Fatal("clamped edge stopped the workspace switch")
		}
		if len(traces) != 0 {
			t.Fatal("clamped edge emitted an event")
		}
	}
}

func TestScreenPolicyExplicitWorkspaceNavigation(t *testing.T) {
	for _, overflow := range []Overflow{OverflowScroll, OverflowCascade} {
		t.Run(string(overflow), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, _ := flashWorkspaces(t, overflow)
				c.cfg.Animations.On = false
				m := c.cur().mon
				m.Focus(0)
				c.applyAction(ActionFocusWorkspaceNext)
				if m.Active != 1 {
					t.Fatal("explicit workspace action blocked")
				}
				m.Focus(0)
				swipe := wsSwipe{c: c}
				swipe.begin()
				if overflow == OverflowScroll {
					swipe.move(12, 30)
				} else {
					for range 12 {
						swipe.at += 8 * time.Millisecond
						c.swipeUpdate(ports.SwipeUpdate{DX: 30, Time: swipe.at})
					}
				}
				if c.swipe.mode != swipeDiscrete {
					t.Fatal("expected discrete swipe")
				}
				swipe.end(false)
				if m.Active != 1 {
					t.Fatalf("workspace swipe blocked: active %d", m.Active)
				}
			})
		})
	}
}
