package core

import "testing"

func spanWorkspace(mode Overflow, n int) *Workspace {
	w := workspace()
	w.Overflow = mode
	w.MaxColumns = 3
	w.SetOutput(1000, 800)
	w.SetGaps(10)
	for i := 0; i < n; i++ {
		w.AddWindow(WindowID(i + 1))
	}
	w.View = 0
	w.view = slide{}
	return w
}

func stampSpans(w *Workspace, first, last int) {
	for i := first; i <= last; i++ {
		w.Columns[i].Span = 10000 / (last - first + 1)
		w.Columns[i].spanLane = last - first + 1
	}
	w.Columns[last].Span += 10000 % (last - first + 1)
}

func TestSpansMutations(t *testing.T) {
	for _, mode := range []Overflow{OverflowFixed, OverflowCascade, OverflowScroll} {
		for _, mutation := range []string{"add", "remove", "take", "receive", "swap", "grow", "shrink", "action"} {
			t.Run(string(mode)+"/"+mutation, func(t *testing.T) {
				w := spanWorkspace(mode, 2)
				stampSpans(w, 0, 1)
				switch mutation {
				case "add":
					w.AddWindow(3)
				case "remove":
					w.RemoveWindow(1)
				case "take":
					col, ok := w.takeColumn()
					if !ok {
						t.Fatal("take")
					}
					other := spanWorkspace(mode, 1)
					other.receive(col, -1)
					for _, c := range other.Columns {
						if c.Span != 0 {
							t.Fatal("destination kept stale span")
						}
					}
				case "receive":
					w.receive(Column{Windows: []WindowID{3}}, 1)
				case "swap":
					w.Focus = 0
					w.MoveColumn(1)
				case "grow":
					w.GrowColumn(false)
				case "shrink":
					w.ShrinkColumn()
				case "action":
					w.Apply("set-column-width +1")
				}
				for _, c := range w.Columns {
					if mutation == "swap" {
						if c.Span == 0 {
							t.Fatal("swap discarded valid spans")
						}
					} else if c.Span != 0 || c.spanLane != 0 {
						t.Fatal("stale span", c)
					}
				}
			})
		}
	}
}

func TestSpansCascadeIsolation(t *testing.T) {
	w := spanWorkspace(OverflowCascade, 5)
	stampSpans(w, 0, 2)
	stampSpans(w, 3, 4)
	w.RemoveWindow(w.Columns[0].Windows[0])
	if w.Columns[0].Span != 0 || w.Columns[1].Span != 0 {
		t.Fatal("changed band retained spans")
	}
	if w.Columns[2].Span != 5000 || w.Columns[3].Span != 5000 {
		t.Fatal("unrelated band reset")
	}
}

func TestSpanWidths(t *testing.T) {
	for _, mode := range []Overflow{OverflowFixed, OverflowCascade, OverflowScroll} {
		w := spanWorkspace(mode, 2)
		stampSpans(w, 0, 1)
		w.Columns[0].Span = 3000
		w.Columns[1].Span = 7000
		rs := w.columnRects()
		avail := 970
		if mode == OverflowScroll {
			avail = 980
		}
		if rs[0].W != avail*3/10 {
			t.Fatal(mode, rs)
		}
		w.SetOutput(1400, 800)
		if w.columnRects()[0].W != (avail+400)*3/10 {
			t.Fatal("ratio lost", mode, w.columnRects())
		}
		w.Columns[0].FullWidth = true
		if w.columnWidth(0) != 1380 {
			t.Fatal("special width lost")
		}
	}
	w := spanWorkspace(OverflowFixed, 4)
	stampSpans(w, 0, 3)
	w.scroll()
	for _, c := range w.Columns {
		if c.Span != 0 {
			t.Fatal("spiral spans")
		}
	}
}

func TestSpansDropIntoLane(t *testing.T) {
	for _, mode := range []Overflow{OverflowFixed, OverflowCascade, OverflowScroll} {
		w := spanWorkspace(mode, 2)
		stampSpans(w, 0, 1)
		src := spanWorkspace(mode, 1)
		src.Columns[0].Windows[0] = 9
		m := monitor()
		m.Workspaces = []*Workspace{w, src}
		sc := &screen{mon: m}
		c := &Core{screens: []*screen{sc}}
		c.drop(9, dropTarget{kind: dropInsert, screen: sc, ws: w, anchor: w.Columns[1].Windows[0]})
		if len(w.Columns) != 3 {
			t.Fatal("not dropped", mode, w.Columns)
		}
		for _, col := range w.Columns {
			if col.Span != 0 || col.spanLane != 0 {
				t.Fatal("drop retained stamp", mode, col)
			}
		}
	}
}
