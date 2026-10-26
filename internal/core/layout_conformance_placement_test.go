package core

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core/layout/layoutmocks"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/stretchr/testify/mock"
)

func TestLayoutPlacementConformance(t *testing.T) {
	for _, l := range conformanceLayouts() {
		mode := l.Name()
		t.Run(string(mode), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				for k := 1; k <= 4; k++ {
					for _, n := range []int{0, 1, k, k + 1, 2*k + 3} {
						w := Workspace{Lanes: layout.Lanes{Overflow: mode, Override: l, MaxColumns: k, Usable: Rect{W: 1000, H: 800}}}
						for i := 0; i < n; i++ {
							w.AddWindow(WindowID(i + 1))
						}
						check := func() {
							t.Helper()
							keys := make(map[uint64]bool)
							for _, c := range w.Columns {
								if c.Key == 0 || keys[c.Key] {
									t.Fatalf("invalid column key %d", c.Key)
								}
								keys[c.Key] = true
							}

						}
						check()
						for focus := 0; focus < max(n, 1); focus++ {
							w.Focus = focus
							for _, full := range []bool{false, true} {
								if n > 0 {
									w.Columns[focus].FullWidth = full
								}
								v := w.Snapshot()
								at, _ := l.SpawnAt(v)
								if at < 0 || at > n {
									t.Fatalf("spawn %d outside [0,%d]", at, n)
								}
								at = l.CoveredSpawnAt(v)
								if at < 0 || at > n || w.Focus != focus {
									t.Fatal("covered spawn changed focus or returned invalid index")
								}
								if n > 0 {
									w.Columns[focus].FullWidth = false
								}
							}
						}
						w.Focus = 0
						w.insertAt(0, Column{Windows: []WindowID{999}})
						check()
						if len(w.Columns) > 1 {
							w.Focus = 0
							w.MoveColumn(1)
							check()
						}
						w.DeleteColumn(0)
						check()
					}
				}

			})
		})
	}
}

// What a state hands out on removal reaches the next workspace's state, and
// is cleared from the column once received.
func TestColumnCarryTravelsThroughStates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := layoutmocks.NewMockState(t)
		dst := layoutmocks.NewMockState(t)
		from := Workspace{Lanes: layout.Lanes{Override: extensionLayout(overflowTrail, src), Columns: []Column{{Windows: []WindowID{1}, Key: 1}}}}
		to := Workspace{Lanes: layout.Lanes{Override: extensionLayout(overflowTrail, dst)}}
		src.EXPECT().Removed(0, mock.Anything).Return(42).Once()
		col, ok := from.takeColumn()
		if !ok || col.Carry != 42 {
			t.Fatalf("taken column carry %v", col.Carry)
		}
		dst.EXPECT().Inserted(&to.Lanes, 0, layout.InsertJoinDisplaced, 42).Return(nil).Once()
		to.insertAt(0, col)
		if to.Columns[0].Carry != nil {
			t.Fatal("carry not cleared once received")
		}

	})
}
