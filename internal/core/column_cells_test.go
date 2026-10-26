package core

import (
	"reflect"
	"testing"
	"testing/synctest"
)

// cellsWorkspace is n columns on an 800 px wide output, no gaps, so a cell
// of max-columns k is 800/k px.
func cellsWorkspace(o Overflow, k, n int) *Workspace {
	w := &Workspace{Lanes: Lanes{Overflow: o, MaxColumns: k}}
	w.SetOutput(800, 600)
	for id := 1; id <= n; id++ {
		w.AddWindow(WindowID(id))
	}
	return w
}

func cellsOf(w *Workspace) []int {
	got := make([]int, len(w.Columns))
	for i, c := range w.Columns {
		got[i] = c.Cells
	}
	return got
}

func widthsOf(w *Workspace) []int {
	got := make([]int, len(w.Columns))
	for i, r := range w.columnRects() {
		got[i] = r.W
	}
	return got
}

// The grow cycle of cycle-column-width, in the three overflows (AC1 to AC4).
func TestGrowColumnCycle(t *testing.T) {
	type step struct {
		cells  []int
		widths []int // nil: not checked
	}
	for _, tc := range []struct {
		name  string
		o     Overflow
		k, n  int
		focus int // window ID
		steps []step
	}{
		{"cascade two columns", OverflowCascade, 4, 2, 2, []step{
			{[]int{1, 3}, []int{200, 600}},
			{[]int{0, 0}, []int{400, 400}},
			{[]int{1, 3}, []int{200, 600}},
		}},
		{"fixed two columns", OverflowFixed, 4, 2, 2, []step{
			{[]int{1, 3}, []int{200, 600}},
			{[]int{0, 0}, []int{400, 400}},
		}},
		{"cascade three columns", OverflowCascade, 4, 3, 2, []step{
			{[]int{1, 2, 1}, []int{200, 400, 200}},
			{[]int{0, 0, 0}, []int{266, 266, 268}},
		}},
		{"fixed three columns", OverflowFixed, 4, 3, 2, []step{
			{[]int{1, 2, 1}, []int{200, 400, 200}},
			{[]int{0, 0, 0}, []int{266, 266, 268}},
		}},
		{"remainder goes to the focused column and is the step", OverflowCascade, 5, 3, 1, []step{
			// 5 cells over 3 columns: 1 each and the 2 left to the focused one.
			{[]int{3, 1, 1}, nil},
			// No column has a cell to give: equal shares again.
			{[]int{0, 0, 0}, nil},
			{[]int{3, 1, 1}, nil},
		}},
		{"an even lane steps at once, the nearest tied donor first", OverflowCascade, 6, 3, 3, []step{
			{[]int{2, 1, 3}, nil},
			{[]int{1, 1, 4}, nil},
			{[]int{0, 0, 0}, nil},
		}},
		{"scroll unset column", OverflowScroll, 4, 2, 2, []step{
			{[]int{0, 3}, []int{400, 600}},
			{[]int{0, 4}, []int{400, 800}},
			{[]int{0, 0}, []int{400, 400}},
			{[]int{0, 3}, []int{400, 600}},
		}},
		{"scroll lone column already fills the width", OverflowScroll, 4, 1, 1, []step{
			{[]int{0}, []int{800}},
		}},
		{"scroll three columns", OverflowScroll, 4, 3, 1, []step{
			{[]int{2, 0, 0}, nil},
			{[]int{3, 0, 0}, nil},
			{[]int{4, 0, 0}, nil},
			{[]int{0, 0, 0}, nil},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := cellsWorkspace(tc.o, tc.k, tc.n)
				w.FocusID(WindowID(tc.focus))
				for i, s := range tc.steps {
					w.Apply(ActionCycleColumnWidth)
					if got := cellsOf(w); !reflect.DeepEqual(got, s.cells) {
						t.Fatalf("step %d: cells %v, want %v", i, got, s.cells)
					}
					if s.widths != nil {
						if got := widthsOf(w); !reflect.DeepEqual(got, s.widths) {
							t.Fatalf("step %d: widths %v, want %v", i, got, s.widths)
						}
					}
				}

			})
		})
	}
}

// A lone column and a full lane do not change (AC3).
func TestGrowColumnNoop(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    Overflow
		k, n int
	}{
		{"cascade lone", OverflowCascade, 4, 1},
		{"fixed lone", OverflowFixed, 4, 1},
		{"fixed full row", OverflowFixed, 4, 4},
		{"fixed spiral", OverflowFixed, 2, 4},
		{"max-columns 1", OverflowCascade, 1, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := cellsWorkspace(tc.o, tc.k, tc.n)
				before := w.columnRects()
				for _, a := range []Action{ActionCycleColumnWidth, "set-column-width +1", "set-column-width -1"} {
					w.Apply(a)
					if got := cellsOf(w); !reflect.DeepEqual(got, make([]int, tc.n)) {
						t.Fatalf("%s: cells %v", a, got)
					}
					if got := w.columnRects(); !reflect.DeepEqual(got, before) {
						t.Fatalf("%s: rects %v, want %v", a, got, before)
					}
				}

			})
		})
	}
}

// set-column-width +N/-N grows or shrinks by cells without cycling, and a
// column never goes below one cell (AC5).
func TestSetColumnWidthCells(t *testing.T) {
	for _, o := range []Overflow{OverflowCascade, OverflowFixed} {
		t.Run(string(o), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := cellsWorkspace(o, 4, 2)
				w.FocusID(2)
				for _, s := range []struct {
					a     Action
					cells []int
				}{
					{"set-column-width +1", []int{1, 3}},
					{"set-column-width +1", []int{1, 3}}, // no donor: no cycle
					{"set-column-width -1", []int{2, 2}},
					{"set-column-width -1", []int{3, 1}},
					{"set-column-width -1", []int{3, 1}}, // never below one cell
					{"set-column-width +2", []int{1, 3}},
					{"set-column-width -3", []int{3, 1}},
				} {
					w.Apply(s.a)
					if got := cellsOf(w); !reflect.DeepEqual(got, s.cells) {
						t.Fatalf("%s: cells %v, want %v", s.a, got, s.cells)
					}
				}
				// The leftmost column at the lane's end gives to its left neighbor.
				w.FocusID(1)
				w.Apply("set-column-width -1")
				if got := cellsOf(w); !reflect.DeepEqual(got, []int{2, 2}) {
					t.Fatalf("cells %v", got)
				}

			})
		})
	}
	t.Run("shrink gives to the right, or to the left at the end", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := cellsWorkspace(OverflowCascade, 6, 3)
			w.Columns[0].Cells, w.Columns[1].Cells, w.Columns[2].Cells = 2, 2, 2
			w.FocusID(2)
			w.Apply("set-column-width -1")
			if got := cellsOf(w); !reflect.DeepEqual(got, []int{2, 1, 3}) {
				t.Fatalf("cells %v", got)
			}
			w.FocusID(3)
			w.Apply("set-column-width -1")
			if got := cellsOf(w); !reflect.DeepEqual(got, []int{2, 2, 2}) {
				t.Fatalf("cells %v", got)
			}

		})
	})
	t.Run("unnormalized lane: the shrink gives the remainder to the leftmost", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := cellsWorkspace(OverflowCascade, 5, 3)
			w.FocusID(3)
			w.Apply("set-column-width -1") // base 2,2,1 and column 3 has one cell
			if got := cellsOf(w); !reflect.DeepEqual(got, []int{0, 0, 0}) {
				t.Fatalf("cells %v: nothing is written when nothing changes", got)
			}
			w.FocusID(2)
			w.Apply("set-column-width -1") // 2,2,1 -> 2,1,2
			if got := cellsOf(w); !reflect.DeepEqual(got, []int{2, 1, 2}) {
				t.Fatalf("cells %v", got)
			}

		})
	})
}

func TestGrowColumnScrollBounds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := cellsWorkspace(OverflowScroll, 4, 2)
		w.FocusID(2)
		w.Apply("set-column-width -1") // auto is 2 cells
		if got := cellsOf(w); !reflect.DeepEqual(got, []int{0, 1}) {
			t.Fatalf("cells %v", got)
		}
		for range 3 {
			w.Apply("set-column-width -1")
		}
		if got := cellsOf(w); !reflect.DeepEqual(got, []int{0, 1}) {
			t.Fatalf("below one cell: %v", got)
		}
		for range 6 {
			w.Apply("set-column-width +1")
		}
		if got := cellsOf(w); !reflect.DeepEqual(got, []int{0, 4}) {
			t.Fatalf("above max-columns: %v", got)
		}

	})
}

func TestGrowColumnEdgeCases(t *testing.T) {
	t.Run("a float focused changes nothing", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			for _, o := range []Overflow{OverflowScroll, OverflowCascade, OverflowFixed} {
				w := cellsWorkspace(o, 4, 2)
				w.AddFloating(9, 100, 100)
				w.Apply(ActionCycleColumnWidth)
				w.Apply("set-column-width +1")
				w.Apply("set-column-width -1")
				if got := cellsOf(w); !reflect.DeepEqual(got, []int{0, 0}) {
					t.Fatalf("%s: cells %v", o, got)
				}
			}

		})
	})
	t.Run("a maximized column unmaximizes, then grows", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			for _, o := range []Overflow{OverflowScroll, OverflowCascade, OverflowFixed} {
				w := cellsWorkspace(o, 4, 2)
				w.ToggleFullWidth()
				w.Apply(ActionCycleColumnWidth)
				if w.Columns[w.Focus].FullWidth || w.Columns[w.Focus].Cells != 3 {
					t.Fatalf("%s: %+v", o, w.Columns)
				}
			}

		})
	})
	t.Run("a fullscreen window hides the layout: no edit", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := cellsWorkspace(OverflowFixed, 4, 2)
			w.ToggleFullscreen()
			w.Apply(ActionCycleColumnWidth)
			if got := cellsOf(w); !reflect.DeepEqual(got, []int{0, 0}) {
				t.Fatalf("cells %v", got)
			}

		})
	})
	t.Run("the donor is the largest, then the nearest, right first", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := cellsWorkspace(OverflowCascade, 5, 3)
			w.Columns[0].Cells, w.Columns[1].Cells, w.Columns[2].Cells = 2, 1, 2
			w.FocusID(2)
			w.Apply(ActionCycleColumnWidth)
			if got := cellsOf(w); !reflect.DeepEqual(got, []int{2, 2, 1}) {
				t.Fatalf("tie: cells %v", got)
			}
			// C is focused with {2,2,1}: A and B tie, the nearer one gives.
			w.FocusID(3)
			w.Apply(ActionCycleColumnWidth)
			if got := cellsOf(w); !reflect.DeepEqual(got, []int{2, 1, 2}) {
				t.Fatalf("largest: cells %v", got)
			}
			w.Columns[0].Cells, w.Columns[1].Cells, w.Columns[2].Cells = 3, 1, 1
			w.FocusID(2)
			w.Apply(ActionCycleColumnWidth)
			if got := cellsOf(w); !reflect.DeepEqual(got, []int{2, 2, 1}) {
				t.Fatalf("largest: cells %v", got)
			}

		})
	})
	t.Run("only the focused band changes", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := cellsWorkspace(OverflowCascade, 4, 6)
			// Band 1 holds columns 5 and 6.
			w.Apply(ActionCycleColumnWidth)
			if got := cellsOf(w); !reflect.DeepEqual(got, []int{0, 0, 0, 0, 1, 3}) {
				t.Fatalf("cells %v", got)
			}
			if got := widthsOf(w); !reflect.DeepEqual(got, []int{200, 200, 200, 200, 200, 600}) {
				t.Fatalf("widths %v", got)
			}
			w.Apply(ActionCycleColumnWidth)
			if got := cellsOf(w); !reflect.DeepEqual(got, []int{0, 0, 0, 0, 0, 0}) {
				t.Fatalf("cells %v", got)
			}
			w.FocusID(1) // A full band pushes a neighbor down (cascade_cycle_test).
			w.Apply(ActionCycleColumnWidth)
			if got := cellsOf(w)[4:]; !reflect.DeepEqual(got, []int{0, 0}) {
				t.Fatalf("other band changed: cells %v", cellsOf(w))
			}

		})
	})
}

// A column set above max-columns (moved to a narrower workspace, or after a
// reload) renders proportionally and cycles back to auto.
func TestCellsAboveMaxColumns(t *testing.T) {
	t.Run("lane", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			for _, o := range []Overflow{OverflowCascade, OverflowFixed} {
				w := cellsWorkspace(o, 2, 2)
				w.Columns[0].Cells = 3
				if got := widthsOf(w); !reflect.DeepEqual(got, []int{600, 200}) {
					t.Fatalf("%s: widths %v", o, got)
				}
				if o == OverflowCascade {
					continue
				}
				// A narrower K does not rewrite the stored cells.
				if got := cellsOf(w); !reflect.DeepEqual(got, []int{3, 0}) {
					t.Fatalf("cells %v", got)
				}
			}

		})
	})
	t.Run("a full uneven lane cycles back to equal", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			for _, o := range []Overflow{OverflowCascade, OverflowFixed} {
				w := cellsWorkspace(o, 4, 4)
				w.Columns[1].Cells = 2
				w.Columns[0].Cells, w.Columns[2].Cells = 1, 1
				w.Apply("set-column-width +1")
				if got := cellsOf(w); !reflect.DeepEqual(got, []int{1, 2, 1, 0}) {
					t.Fatalf("%s: set-column-width cycled: %v", o, got)
				}
				w.Apply(ActionCycleColumnWidth)
				if got := cellsOf(w); !reflect.DeepEqual(got, []int{0, 0, 0, 0}) {
					t.Fatalf("%s: cells %v", o, got)
				}
			}

		})
	})
	t.Run("a lone column with cells cycles back to auto", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			for _, o := range []Overflow{OverflowCascade, OverflowFixed} {
				w := cellsWorkspace(o, 4, 1)
				w.Columns[0].Cells = 2
				w.Apply(ActionCycleColumnWidth)
				if got := cellsOf(w); !reflect.DeepEqual(got, []int{0}) {
					t.Fatalf("%s: cells %v", o, got)
				}
			}

		})
	})
	t.Run("scroll", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := cellsWorkspace(OverflowScroll, 2, 2)
			w.Columns[0].Cells = 3
			w.FocusID(1)
			if got := widthsOf(w)[0]; got != 800 {
				t.Fatalf("width %d, at most max-columns cells", got)
			}
			w.Apply(ActionCycleColumnWidth)
			if got := cellsOf(w); !reflect.DeepEqual(got, []int{0, 0}) {
				t.Fatalf("cells %v", got)
			}

		})
	})
	t.Run("a moved column keeps its cells", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			src := cellsWorkspace(OverflowScroll, 4, 2)
			src.Columns[1].Cells = 3
			dst := cellsWorkspace(OverflowScroll, 2, 1)
			col, ok := src.takeColumn()
			if !ok {
				t.Fatal("no column")
			}
			dst.receive(col, 1)
			if got := cellsOf(dst); !reflect.DeepEqual(got, []int{0, 3}) {
				t.Fatalf("cells %v", got)
			}

		})
	})
}

// The overview blocks the grow and resize binds, like the other window
// mutations, and its cascade preview keeps the columns' cells.
func TestOverviewKeepsCells(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := newMonitor("", "")
		m.SetOutput(900, 600)
		m.SetOverflow(OverflowCascade)
		m.SetMaxColumns(4)
		for id := WindowID(1); id <= 2; id++ {
			m.AddWindow(id)
		}
		w := m.Current()
		w.Columns[0].Cells, w.Columns[1].Cells = 1, 3
		screen := w.columnRects()
		if screen[0].W != 225 || screen[1].W != 675 {
			t.Fatalf("screen widths %v, want 225 and 675", screen)
		}
		m.ToggleOverview()
		tiles, _, _ := w.previewTiles()
		for i, p := range tiles {
			if want := screen[i]; p.Rect.W != want.W || p.Rect.X != want.X-w.Usable.X || p.Rect.H != want.H {
				t.Fatalf("column %d: preview %+v, screen %+v", i, p.Rect, want)
			}
		}
		for _, a := range []Action{ActionCycleColumnWidth, "set-column-width +1", "set-column-width -1"} {
			m.Apply(a)
			if got := cellsOf(w); !reflect.DeepEqual(got, []int{1, 3}) {
				t.Fatalf("%s in the overview: cells %v", a, got)
			}
		}

	})
}
