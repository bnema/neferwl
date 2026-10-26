package core

import (
	"slices"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/mock"
)

func TestLayoutPlacementConformance(t *testing.T) {
	for _, l := range conformanceLayouts() {
		mode := l.Name()
		t.Run(string(mode), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				for k := 1; k <= 4; k++ {
					for _, n := range []int{0, 1, k, k + 1, 2*k + 3} {
						w := Workspace{Lanes: Lanes{Overflow: mode, Override: l, MaxColumns: k, Usable: Rect{W: 1000, H: 800}}}
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
		src := NewMockState(t)
		dst := NewMockState(t)
		from := Workspace{Lanes: Lanes{lstate: src, Columns: []Column{{Windows: []WindowID{1}, Key: 1}}}}
		to := Workspace{Lanes: Lanes{lstate: dst}}
		src.EXPECT().Removed(0, mock.Anything).Return(42).Once()
		col, ok := from.takeColumn()
		if !ok || col.Carry != 42 {
			t.Fatalf("taken column carry %v", col.Carry)
		}
		dst.EXPECT().Inserted(&to.Lanes, 0, InsertJoinDisplaced, 42).Return(nil).Once()
		to.insertAt(0, col)
		if to.Columns[0].Carry != nil {
			t.Fatal("carry not cleared once received")
		}

	})
}

// The insert kind tells the cascade state what a new column means for bands:
// its own band, the band position of the column it displaces, or nothing.
func TestCascadeInsertKinds(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind InsertKind
		want []bool // band starts after inserting at 1, before: [true, false, true]
	}{
		{"default", InsertDefault, []bool{true, false, false, true}},
		{"new lane", InsertNewLane, []bool{true, true, false, true}},
		{"join displaced", InsertJoinDisplaced, []bool{true, false, false, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := Workspace{Lanes: Lanes{Overflow: OverflowCascade, MaxColumns: 3}}
				for i := WindowID(1); i <= 3; i++ {
					w.Columns = append(w.Columns, Column{Windows: []WindowID{i}})
				}
				w.SettleLayout()
				w.setBandStart(0, true)
				w.setBandStart(1, false)
				w.setBandStart(2, true)
				w.Columns = slices.Insert(w.Columns, 1, Column{Windows: []WindowID{9}})
				w.ColumnInserted(1, tc.kind)
				for i, want := range tc.want {
					if got := w.BandStart(i); got != want {
						t.Fatalf("band start %d = %v, want %v (all: %v)", i, got, want, w.cascadeState().bandStart)
					}
				}

			})
		})
	}
	// Inserting before a band start hands the start to the new column.
	w := Workspace{Lanes: Lanes{Overflow: OverflowCascade, MaxColumns: 3}}
	for i := WindowID(1); i <= 3; i++ {
		w.Columns = append(w.Columns, Column{Windows: []WindowID{i}})
	}
	w.SettleLayout()
	w.setBandStart(2, true)
	w.Columns = slices.Insert(w.Columns, 2, Column{Windows: []WindowID{9}})
	w.ColumnInserted(2, InsertJoinDisplaced)
	if !w.BandStart(2) || w.BandStart(3) {
		t.Fatalf("band starts %v: the new column must take the displaced start", w.cascadeState().bandStart)
	}
}
