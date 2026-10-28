package workspace

import (
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/core/layout/layoutmocks"
	"github.com/stretchr/testify/mock"
)

func TestCarouselSlot(t *testing.T) {
	tests := []struct {
		name  string
		at, n int
		wraps bool
		want  []int // slot of card 0..n-1
	}{
		{"no wrap odd centre", 2, 5, false, []int{-2, -1, 0, 1, 2}},
		{"no wrap first", 0, 4, false, []int{0, 1, 2, 3}},
		{"no wrap last", 3, 4, false, []int{-3, -2, -1, 0}},
		{"wrap odd centre", 2, 5, true, []int{-2, -1, 0, 1, 2}},
		{"wrap odd first", 0, 5, true, []int{0, 1, 2, -2, -1}},
		{"wrap odd last", 4, 5, true, []int{1, 2, -2, -1, 0}},
		{"wrap even first: left takes extra", 0, 4, true, []int{0, 1, -2, -1}},
		{"wrap even middle", 2, 4, true, []int{-2, -1, 0, 1}},
		{"wrap even last", 3, 4, true, []int{1, -2, -1, 0}},
		{"wrap two cards: other is left", 0, 2, true, []int{0, -1}},
		{"wrap two cards at 1", 1, 2, true, []int{-1, 0}},
		{"single no wrap", 0, 1, false, []int{0}},
		{"single wrap", 0, 1, true, []int{0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := make([]int, tt.n)
			for i := range got {
				got[i] = CarouselSlot(i, tt.at, tt.n, tt.wraps)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("slots %v, want %v", got, tt.want)
			}
		})
	}
	t.Run("at card is slot 0", func(t *testing.T) {
		for n := 1; n <= 9; n++ {
			for at := 0; at < n; at++ {
				for _, wraps := range []bool{false, true} {
					if s := CarouselSlot(at, at, n, wraps); s != 0 {
						t.Fatalf("n=%d at=%d wraps=%v: slot %d", n, at, wraps, s)
					}
				}
			}
		}
	})
	// n == 0 has no card: wrapping would divide by zero, so only the
	// non-wrapping form is meaningful and it is the plain offset.
	t.Run("empty no wrap", func(t *testing.T) {
		if s := CarouselSlot(0, 0, 0, false); s != 0 {
			t.Fatalf("slot %d, want 0", s)
		}
	})
}

// TestNewFromDropsLayoutState pins that a copy made by NewFrom does not share
// or inherit the template's lazily built layout state.
func TestNewFromDropsLayoutState(t *testing.T) {
	var built []*layoutmocks.MockState
	newState := func() layout.State {
		s := layoutmocks.NewMockState(t)
		s.EXPECT().Inserted(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
		s.EXPECT().Removed(mock.Anything, mock.Anything).Return(nil).Maybe()
		s.EXPECT().Settle(mock.Anything).Return().Maybe()
		built = append(built, s)
		return s
	}
	newWS := func() *Workspace {
		return &Workspace{Lanes: layout.Lanes{Overflow: overflowCentered, Override: extensionLayoutWith(overflowCentered, newState),
			MaxColumns: 4, Usable: Rect{X: 40, Y: 60, W: 1000, H: 720}, Output: Rect{W: 1200, H: 900}, Gaps: 8}}
	}
	fill := func(w *Workspace) {
		w.AddWindow(1)
		w.AddWindow(2)
		_ = w.Layout()
	}

	tmpl := newWS()
	fill(tmpl)
	if len(built) != 1 {
		t.Fatalf("template built %d layout states, want 1", len(built))
	}

	cp := NewFrom(tmpl, 7)
	if len(cp.Columns) != 0 || len(cp.Layout()) != 0 {
		t.Fatalf("copy not empty: %d columns", len(cp.Columns))
	}
	fill(cp)
	if len(built) != 2 {
		t.Fatalf("copy reused the template's layout state: %d states built, want 2", len(built))
	}

	fresh := newWS()
	fill(fresh)
	if got, want := cp.Layout(), fresh.Layout(); !slices.Equal(got, want) {
		t.Fatalf("copy lays out %v, fresh workspace %v", got, want)
	}
}
