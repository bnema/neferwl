package layout

import (
	"slices"
	"testing"
	"testing/synctest"
)

// Private state invariants belong beside the engine; Workspace tests check the
// visible transfer/restore behavior through the exported lane contract.
func TestCascadeCarryRekeysAndTransfersHistory(t *testing.T) {
	from := Lanes{Overflow: OverflowCascade, MaxColumns: 3,
		Columns: []Column{{Windows: []WindowID{1}}, {Windows: []WindowID{2}}, {Windows: []WindowID{3}}}}
	from.SettleLayout()
	col := from.Columns[1]
	from.cascadeCol(col).pushedDown = [][]WindowID{{3}}
	carry := from.DeleteColumn(1)
	if _, exists := from.cascadeState().cols[col.Key]; exists {
		t.Fatal("source retained transferred history")
	}
	to := Lanes{Overflow: OverflowCascade, MaxColumns: 3, colSeq: col.Key, Columns: []Column{col}}
	to.Columns[0].Carry = carry
	to.ColumnInserted(0, InsertDefault)
	received := to.Columns[0]
	if received.Key == col.Key || received.Carry != nil {
		t.Fatalf("incoming identity/carry not consumed: %+v", received)
	}
	state := to.cascadeState().cols[received.Key]
	if state == nil || len(state.pushedDown) != 1 || !slices.Equal(state.pushedDown[0], []WindowID{3}) {
		t.Fatalf("history did not reach destination: %+v", state)
	}
}

func TestCascadeIsolateRemembersOwnBand(t *testing.T) {
	w := Lanes{Overflow: OverflowCascade, MaxColumns: 3,
		Columns: []Column{{Windows: []WindowID{1}}, {Windows: []WindowID{2}}}}
	w.SettleLayout()
	if !w.Engine().Isolate(Edit{Lanes: &w, Focused: 1}, 2) {
		t.Fatal("window was not isolated")
	}
	if !w.cascadeCol(w.Columns[w.ColumnOf(2)]).ownBand {
		t.Fatal("isolated column lost its independent band")
	}
}

func TestCascadeBoundariesFollowColumnEdits(t *testing.T) {
	for _, kind := range []InsertKind{InsertDefault, InsertNewLane, InsertJoinDisplaced} {
		w := Lanes{Overflow: OverflowCascade, MaxColumns: 2}
		for i := range 9 {
			w.Columns = append(w.Columns, Column{Windows: []WindowID{WindowID(i + 1)}})
			w.ColumnInserted(i, kind)
			w.SettleLayout()
			if got := len(w.cascadeState().bandStart); got != len(w.Columns) {
				t.Fatalf("insert: boundaries %d, columns %d", got, len(w.Columns))
			}
		}
		for len(w.Columns) > 0 {
			w.DeleteColumn(0)
			w.SettleLayout()
			if got := len(w.cascadeState().bandStart); got != len(w.Columns) {
				t.Fatalf("remove: boundaries %d, columns %d", got, len(w.Columns))
			}
		}
	}
}

func TestCascadeScrollTargetDoesNotMutateState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := Lanes{Overflow: OverflowCascade, MaxColumns: 2, Usable: Rect{W: 900, H: 600}, Output: Rect{W: 900, H: 600}}
		for i := range 2 {
			w.Columns = append(w.Columns, Column{Windows: []WindowID{WindowID(i + 1)}})
			w.ColumnInserted(i, InsertDefault)
		}
		w.Focus = 1
		w.SettleLayout()
		v := w.Snapshot()
		v.MaxColumns = 1
		w.Engine().ScrollTarget(v)
		if owner := w.Snapshot(); owner.bandStart(1) {
			t.Fatal("snapshot scrollTarget changed owner's boundary")
		}

	})
}
