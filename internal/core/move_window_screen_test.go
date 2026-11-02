package core

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestMoveWindowVerticalScreenEdge(t *testing.T) {
	for _, overflow := range []Overflow{OverflowScroll, OverflowFixed, OverflowCascade} {
		for _, dir := range []int{-1, 1} {
			for _, neighbor := range []bool{false, true} {
				for _, stacked := range []bool{false, true} {
					for _, fullscreen := range []bool{false, true} {
						for _, policy := range []domain.NavEdge{domain.NavEdgeScreen, domain.NavEdgeWorkspace} {
							t.Run(fmt.Sprintf("%s/dir=%d/screen=%t/stacked=%t/fullscreen=%t/policy=%v", overflow, dir, neighbor, stacked, fullscreen, policy), func(t *testing.T) {
								tiles := []tile{{"A", 0, 0, 100, 80}}
								if neighbor {
									tiles = append(tiles, tile{"B", 0, dir * 80, 100, 80})
								}
								c := layoutCore(0, tiles...)
								c.cfg.Navigation.Edge = policy
								m := c.cur().mon
								m.SetOverflow(overflow)
								m.SetMaxColumns(2)
								m.AddWindow(1)
								if stacked {
									m.AddWindow(2)
									m.Current().ConsumeOrExpel(-1)
								}
								w := m.Current()
								id := WindowID(1)
								if stacked && dir > 0 {
									id = 2
								}
								w.FocusID(id)
								if fullscreen {
									w.ToggleFullscreen()
								}
								var to *Workspace
								if neighbor {
									dst := c.screens[1].mon
									dst.SetOverflow(overflow)
									dst.SetMaxColumns(2)
									dst.AddWindow(10)
									dst.AddWindow(11)
									to = dst.Current()
									to.FocusID(10)
								}
								action := ActionMoveWindowUp
								if dir > 0 {
									action = ActionMoveWindowDown
								}
								c.applyAction(action)
								crosses := neighbor && overflow != OverflowCascade
								if crosses {
									if c.focusScreen != 1 || w.Has(id) || !to.Has(id) {
										t.Fatal("window did not transfer with focus")
									}
									if len(to.Columns) != 3 || to.Columns[1].Selected() != id {
										t.Fatalf("landing columns %+v", to.Columns)
									}
									if got, _ := to.Focused(); got != id {
										t.Fatalf("destination focus %d", got)
									}
									if fullscreen && to.Fullscreen != id {
										t.Fatal("fullscreen lost")
									}
									if stacked && !fullscreen && len(w.Columns[0].Windows) != 1 {
										t.Fatal("moved more than one window")
									}
								} else {
									if c.focusScreen != 0 || !w.Has(id) {
										t.Fatal("window left without eligible screen")
									}
									if neighbor && (to.Has(id) || len(to.Columns) != 2) {
										t.Fatal("cascade changed neighbor")
									}
								}
								if m.Active != 0 {
									t.Fatal("move changed workspace")
								}
							})
						}
					}
				}
			}
		}
	}
}

func TestMoveWindowLocalBeforeScreen(t *testing.T) {
	for _, overflow := range []Overflow{OverflowScroll, OverflowFixed} {
		for _, dir := range []int{-1, 1} {
			c := layoutCore(0, tile{"A", 0, 0, 100, 80}, tile{"B", 0, dir * 80, 100, 80})
			m := c.cur().mon
			m.SetOverflow(overflow)
			m.AddWindow(1)
			m.AddWindow(2)
			m.Current().ConsumeOrExpel(-1)
			id, action := WindowID(1), ActionMoveWindowDown
			if dir < 0 {
				id, action = 2, ActionMoveWindowUp
			}
			m.Current().FocusID(id)
			c.applyAction(action)
			if c.focusScreen != 0 || !reflect.DeepEqual(m.Current().Columns[0].Windows, []WindowID{2, 1}) {
				t.Fatal("local move did not precede screen transfer")
			}
		}
	}
}

func TestMoveWindowFloatScreenRules(t *testing.T) {
	for _, fullscreen := range []bool{false, true} {
		c := layoutCore(0, tile{"A", 0, 0, 100, 80}, tile{"B", 0, 80, 100, 80})
		m := c.cur().mon
		m.AddFloating(1, 20, 20)
		if fullscreen {
			m.SetFullscreen(1, true)
		}
		c.applyAction(ActionMoveWindowDown)
		if fullscreen {
			to := c.screens[1].mon.Current()
			if c.focusScreen != 1 || to.Fullscreen != 1 || !to.IsFloat(1) {
				t.Fatal("fullscreen float did not follow fullscreen screen rules")
			}
		} else if c.focusScreen != 0 || !m.Current().Has(1) {
			t.Fatal("free float left its screen")
		}
	}
}

func TestMoveWindowEmptyDestination(t *testing.T) {
	c := layoutCore(0, tile{"A", 0, 0, 100, 80}, tile{"B", 0, 80, 100, 80})
	c.cur().mon.AddWindow(1)
	c.applyAction(ActionMoveWindowDown)
	to := c.cur().mon.Current()
	if c.focusScreen != 1 || len(to.Columns) != 1 || to.Columns[0].Selected() != 1 {
		t.Fatal("empty destination landing failed")
	}
}
