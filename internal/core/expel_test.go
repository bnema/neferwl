package core

import (
	"reflect"
	"testing"
)

// cols builds a workspace with the given columns, focus on the last window
// of column focus.
func cols(overflow Overflow, max, focus int, columns ...[]WindowID) *Workspace {
	w := &Workspace{Overflow: overflow, MaxColumns: max}
	w.SetOutput(300, 100)
	for _, c := range columns {
		w.Columns = append(w.Columns, Column{Windows: c, Focus: len(c) - 1})
	}
	w.Focus = focus
	return w
}

func ids(w *Workspace) [][]WindowID {
	var out [][]WindowID
	for _, c := range w.Columns {
		out = append(out, c.Windows)
	}
	return out
}

func TestConsumeOrExpel(t *testing.T) {
	for _, tc := range []struct {
		name     string
		w        *Workspace
		dir      int
		want     [][]WindowID
		stays    bool
		focusCol int
	}{
		{"consume right", cols(OverflowScroll, 2, 0, []WindowID{1}, []WindowID{2}), 1, [][]WindowID{{2, 1}}, true, 0},
		{"consume left", cols(OverflowScroll, 2, 1, []WindowID{1}, []WindowID{2}), -1, [][]WindowID{{1, 2}}, true, 0},
		{"consume at the edge does nothing", cols(OverflowFixed, 3, 1, []WindowID{1}, []WindowID{2}), 1, [][]WindowID{{1}, {2}}, true, 1},
		{"expel right in scroll", cols(OverflowScroll, 1, 0, []WindowID{1, 2}, []WindowID{3}), 1, [][]WindowID{{1}, {2}, {3}}, true, 1},
		{"expel left in scroll", cols(OverflowScroll, 1, 1, []WindowID{1}, []WindowID{2, 3}), -1, [][]WindowID{{1}, {3}, {2}}, true, 1},
		{"expel in fixed under the limit", cols(OverflowFixed, 3, 0, []WindowID{1, 2}, []WindowID{3}), 1, [][]WindowID{{1}, {2}, {3}}, true, 1},
		{"full fixed stacks into the next column", cols(OverflowFixed, 3, 0, []WindowID{1, 2}, []WindowID{3}, []WindowID{4}), 1, [][]WindowID{{1}, {3, 2}, {4}}, true, 1},
		{"full fixed at the edge leaves", cols(OverflowFixed, 3, 2, []WindowID{1}, []WindowID{2}, []WindowID{3, 4}), 1, [][]WindowID{{1}, {2}, {3, 4}}, false, 2},
		{"past the limit counts as full", cols(OverflowFixed, 2, 0, []WindowID{1, 2}, []WindowID{3}, []WindowID{4}), 1, [][]WindowID{{1}, {3, 2}, {4}}, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stays := tc.w.ConsumeOrExpel(tc.dir)
			if got := ids(tc.w); !reflect.DeepEqual(got, tc.want) || stays != tc.stays || tc.w.Focus != tc.focusCol {
				t.Fatal(got, stays, tc.w.Focus)
			}

		})
	}
}

func TestConsumeOrExpelFocusFollows(t *testing.T) {
	w := cols(OverflowFixed, 3, 0, []WindowID{1, 2}, []WindowID{3}, []WindowID{4})
	w.ConsumeOrExpel(1)
	if id, _ := w.Focused(); id != 2 {
		t.Fatal(id)
	}
}

func TestConsumeOrExpelSkips(t *testing.T) {
	t.Run("floating focus", func(t *testing.T) {
		w := cols(OverflowScroll, 2, 0, []WindowID{1, 2})
		w.AddFloating(9, 10, 10)
		w.FocusID(9)
		w.ConsumeOrExpel(1)
		if got := ids(w); !reflect.DeepEqual(got, [][]WindowID{{1, 2}}) {
			t.Fatal(got)
		}
	})
	t.Run("fullscreen", func(t *testing.T) {
		w := cols(OverflowFixed, 3, 0, []WindowID{1, 2})
		w.fullscreen = 2
		w.ConsumeOrExpel(1)
		if got := ids(w); !reflect.DeepEqual(got, [][]WindowID{{1, 2}}) {
			t.Fatal(got)
		}
	})
}

func TestConsumeOrExpelSlots(t *testing.T) {
	slots := func(w *Workspace) []int {
		var out []int
		for _, c := range w.Columns {
			out = append(out, c.Slot)
		}
		return out
	}
	t.Run("the slot follows its window", func(t *testing.T) {
		w := cols(OverflowScroll, 1, 0, []WindowID{1, 2})
		w.Columns[0].Slot, w.Columns[0].Width = 1, Width{Num: 2, Den: 3}
		w.Columns[0].Focus = 0
		w.ConsumeOrExpel(1)
		if got := ids(w); !reflect.DeepEqual(got, [][]WindowID{{2}, {1}}) || !reflect.DeepEqual(slots(w), []int{0, 1}) || w.Columns[1].Width != (Width{Num: 2, Den: 3}) || w.Columns[0].Width != (Width{}) {
			t.Fatal(got, slots(w), w.Columns)
		}
	})
	t.Run("another window leaves the slot", func(t *testing.T) {
		w := cols(OverflowScroll, 1, 0, []WindowID{1, 2})
		w.Columns[0].Slot = 1
		w.ConsumeOrExpel(1)
		if !reflect.DeepEqual(slots(w), []int{1, 0}) {
			t.Fatal(slots(w))
		}
	})
	t.Run("stacking the slot window releases it", func(t *testing.T) {
		w := cols(OverflowFixed, 2, 0, []WindowID{1, 2}, []WindowID{3})
		w.Columns[0].Slot, w.Columns[0].Focus = 1, 0
		w.ConsumeOrExpel(1)
		if got := ids(w); !reflect.DeepEqual(got, [][]WindowID{{2}, {3, 1}}) || !reflect.DeepEqual(slots(w), []int{0, 0}) {
			t.Fatal(got, slots(w))
		}
	})
}

func TestExpelTo(t *testing.T) {
	t.Run("new edge column", func(t *testing.T) {
		w := cols(OverflowFixed, 3, 0, []WindowID{1}, []WindowID{2})
		w.expelTo(9, 1)
		if got := ids(w); !reflect.DeepEqual(got, [][]WindowID{{9}, {1}, {2}}) || w.Focus != 0 {
			t.Fatal(got)
		}
	})
	t.Run("full stacks into the first column", func(t *testing.T) {
		w := cols(OverflowFixed, 2, 1, []WindowID{1}, []WindowID{2})
		w.expelTo(9, 1)
		if id, _ := w.Focused(); !reflect.DeepEqual(ids(w), [][]WindowID{{1, 9}, {2}}) || id != 9 {
			t.Fatal(ids(w), id)
		}
	})
	t.Run("full from the right stacks into the last column", func(t *testing.T) {
		w := cols(OverflowFixed, 2, 0, []WindowID{1}, []WindowID{2}, []WindowID{3})
		w.expelTo(9, -1)
		if id, _ := w.Focused(); !reflect.DeepEqual(ids(w), [][]WindowID{{1}, {2}, {3, 9}}) || id != 9 {
			t.Fatal(ids(w), id)
		}
	})
	t.Run("empty workspace", func(t *testing.T) {
		w := cols(OverflowFixed, 2, 0)
		w.expelTo(9, -1)
		if !reflect.DeepEqual(ids(w), [][]WindowID{{9}}) {
			t.Fatal(ids(w))
		}
	})
}
