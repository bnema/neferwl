package core

import (
	"slices"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/mock"
)

func TestLayoutPlacementConformance(t *testing.T) {
	for _, l := range conformanceLayouts() {
		mode := l.name()
		t.Run(string(mode), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				for k := 1; k <= 4; k++ {
					for _, n := range []int{0, 1, k, k + 1, 2*k + 3} {
						w := Workspace{lanes: lanes{Overflow: mode, lay: l, MaxColumns: k, Usable: Rect{W: 1000, H: 800}}}
						for i := 0; i < n; i++ {
							w.AddWindow(WindowID(i + 1))
						}
						check := func() {
							t.Helper()
							keys := make(map[uint64]bool)
							for _, c := range w.Columns {
								if c.key == 0 || keys[c.key] {
									t.Fatalf("invalid column key %d", c.key)
								}
								keys[c.key] = true
							}
							if s, ok := w.lstate.(*cascadeState); ok && len(s.bandStart) != len(w.Columns) {
								t.Fatalf("boundaries %d, columns %d", len(s.bandStart), len(w.Columns))
							}
						}
						check()
						for focus := 0; focus < max(n, 1); focus++ {
							w.Focus = focus
							for _, full := range []bool{false, true} {
								if n > 0 {
									w.Columns[focus].FullWidth = full
								}
								v := w.lview()
								at, _ := l.spawnAt(v)
								if at < 0 || at > n {
									t.Fatalf("spawn %d outside [0,%d]", at, n)
								}
								at = l.coveredSpawnAt(v)
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
						w.deleteColumn(0)
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
		src := newMocklayoutState(t)
		dst := newMocklayoutState(t)
		from := Workspace{lanes: lanes{lstate: src, Columns: []Column{{Windows: []WindowID{1}, key: 1}}}}
		to := Workspace{lanes: lanes{lstate: dst}}
		src.EXPECT().removed(0, mock.Anything).Return(42).Once()
		col, ok := from.takeColumn()
		if !ok || col.carry != 42 {
			t.Fatalf("taken column carry %v", col.carry)
		}
		dst.EXPECT().inserted(&to.lanes, 0, insertJoinDisplaced, 42).Return(nil).Once()
		to.insertAt(0, col)
		if to.Columns[0].carry != nil {
			t.Fatal("carry not cleared once received")
		}

	})
}

// The insert kind tells the cascade state what a new column means for bands:
// its own band, the band position of the column it displaces, or nothing.
func TestCascadeInsertKinds(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind insertKind
		want []bool // band starts after inserting at 1, before: [true, false, true]
	}{
		{"default", insertDefault, []bool{true, false, false, true}},
		{"new lane", insertNewLane, []bool{true, true, false, true}},
		{"join displaced", insertJoinDisplaced, []bool{true, false, false, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := Workspace{lanes: lanes{Overflow: OverflowCascade, MaxColumns: 3}}
				for i := WindowID(1); i <= 3; i++ {
					w.Columns = append(w.Columns, Column{Windows: []WindowID{i}})
				}
				w.settleLayout()
				w.setBandStart(0, true)
				w.setBandStart(1, false)
				w.setBandStart(2, true)
				w.Columns = slices.Insert(w.Columns, 1, Column{Windows: []WindowID{9}})
				w.columnInserted(1, tc.kind)
				for i, want := range tc.want {
					if got := w.bandStart(i); got != want {
						t.Fatalf("band start %d = %v, want %v (all: %v)", i, got, want, w.cascadeState().bandStart)
					}
				}

			})
		})
	}
	// Inserting before a band start hands the start to the new column.
	w := Workspace{lanes: lanes{Overflow: OverflowCascade, MaxColumns: 3}}
	for i := WindowID(1); i <= 3; i++ {
		w.Columns = append(w.Columns, Column{Windows: []WindowID{i}})
	}
	w.settleLayout()
	w.setBandStart(2, true)
	w.Columns = slices.Insert(w.Columns, 2, Column{Windows: []WindowID{9}})
	w.columnInserted(2, insertJoinDisplaced)
	if !w.bandStart(2) || w.bandStart(3) {
		t.Fatalf("band starts %v: the new column must take the displaced start", w.cascadeState().bandStart)
	}
}
