package core

import (
	"fmt"
	"testing"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/domain"
)

func TestEdgeTarget(t *testing.T) {
	for _, policy := range []domain.NavEdge{domain.NavEdgeScreen, domain.NavEdgeWorkspace} {
		for _, overflow := range []Overflow{OverflowScroll, OverflowFixed, OverflowCascade} {
			for _, axis := range []layout.Axis{layout.Horizontal, layout.Vertical} {
				for _, dir := range []int{-1, 1} {
					for _, neighbor := range []bool{false, true} {
						for _, next := range []string{"occupied", "trailing empty", "out of range"} {
							for _, shown := range []bool{false, true} {
								name := fmt.Sprintf("policy=%v/", policy) + fmt.Sprintf("%s/axis=%v/dir=%d/screen=%t/next=%s/shown=%t", overflow, axis, dir, neighbor, next, shown)
								t.Run(name, func(t *testing.T) {
									d, x, y := dirRight, dir*100, 0
									if dir < 0 {
										d = dirLeft
									}
									if axis == layout.Vertical {
										x, y, d = 0, dir*100, dirDown
										if dir < 0 {
											d = dirUp
										}
									}
									tiles := []tile{{"A", 0, 0, 100, 100}}
									if neighbor {
										tiles = append(tiles, tile{"B", x, y, 100, 100})
									}
									c := layoutCore(0, tiles...)
									c.cfg.Navigation.Edge = policy
									m := c.cur().mon
									m.SetOverflow(overflow)
									// Build the adjacent workspace directly, including a negative
									// empty neighbor, to exercise both signs independently.
									m.Workspaces = []*Workspace{m.newWorkspace(), m.newWorkspace(), m.newWorkspace()}
									m.Active = 1
									switch next {
									case "out of range":
										m.Active = 0
										if dir > 0 {
											m.Active = 2
										}
									case "occupied":
										m.Workspaces[m.Active+dir].AddWindow(1)
									}
									if shown {
										m.shown = m.newWorkspace()
										m.shown.Name = "dev"
									}
									workspaceAxis := axis == layout.Vertical
									if overflow == OverflowCascade {
										workspaceAxis = axis == layout.Horizontal
									}
									want := edgeHop{}
									screen := neighbor && (axis == layout.Horizontal || workspaceAxis)
									switch {
									case screen && axis == layout.Horizontal:
										want = edgeHop{edgeScreen, 1}
									case policy == domain.NavEdgeWorkspace && workspaceAxis && !shown && next == "occupied":
										want = edgeHop{edgeWorkspace, m.Active + dir}
									case screen:
										want = edgeHop{edgeScreen, 1}
									case policy == domain.NavEdgeWorkspace && workspaceAxis && !shown && next != "out of range":
										want = edgeHop{edgeWorkspace, m.Active + dir}
									}
									active, current, shownWS := m.Active, m.Current(), m.shown
									if got := c.edgeTarget(axis, dir, d); got != want {
										t.Fatalf("edgeTarget = %+v, want %+v", got, want)
									}
									if c.focusScreen != 0 || m.Active != active || m.Current() != current || m.shown != shownWS || len(m.Workspaces) != 3 {
										t.Fatal("edgeTarget mutated navigation state")
									}
								})
							}
						}
					}
				}
			}
		}
	}

}
