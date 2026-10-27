package core

import (
	"testing"
	"testing/synctest"
)

func TestSpansDropIntoLane(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, mode := range []Overflow{OverflowFixed, OverflowCascade, OverflowScroll} {
			w := spanWorkspace(mode, 2)
			stampSpans(w, 0, 1)
			src := spanWorkspace(mode, 1)
			src.Columns[0].Windows[0] = 9
			m := monitor()
			m.Workspaces = []*Workspace{w, src}
			sc := &screen{mon: m}
			c := &Core{screens: []*screen{sc}}
			c.drop(9, dropTarget{kind: DropInsert, screen: sc, ws: w, anchor: w.Columns[1].Windows[0]})
			if len(w.Columns) != 3 {
				t.Fatal("not dropped", mode, w.Columns)
			}
			for _, col := range w.Columns {
				if col.Span != 0 || col.SpanStamp != 0 {
					t.Fatal("drop retained stamp", mode, col)
				}
			}
		}

	})
}

func TestSpanLaneIdentity(t *testing.T) {
	for _, action := range []string{"cross-drop", "within-drop", "within-keyboard"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := spanWorkspace(OverflowCascade, 4)
				w.SetMaxColumns(2)
				for i := range w.Columns {
					w.SetBandStart(i, i%2 == 0)
				}
				for _, i := range []int{0, 2} {
					rs := modelColumnRects(w)
					w.MoveBorder(Border{Kind: BorderColumn, Col: i}, rs[i].X+rs[i].W+5)
				}
				for _, col := range w.Columns {
					if col.Span != 5000 {
						t.Fatal("not stamped equally", w.Columns)
					}
				}
				cross := action == "cross-drop"
				if action == "cross-drop" || action == "within-drop" {
					m := monitor()
					m.Workspaces = []*Workspace{w}
					sc := &screen{mon: m}
					c := &Core{screens: []*screen{sc}}
					to := 1
					if cross {
						to = 2
					}
					c.drop(w.Columns[0].Windows[0], dropTarget{kind: DropSwap, screen: sc, ws: w, anchor: w.Columns[to].Windows[0]})
				} else {
					w.Focus = 0
					w.MoveColumn(1)
				}
				w.DropStaleSpans()
				for _, col := range w.Columns {
					if cross && col.Span != 0 {
						t.Fatal("cross-band swap retained stamp", w.Columns)
					}
					if !cross && col.Span != 5000 {
						t.Fatal("within-band swap discarded stamp", w.Columns)
					}
				}

			})
		})
	}
}
