package workspace

import (
	"reflect"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
)

func win(id WindowID, side domain.DropSide, a domain.DropAction) Anchor {
	return Anchor{Kind: AnchorWindow, Side: side, Window: id, Action: a}
}

func TestDropApply(t *testing.T) {
	type c = [][]WindowID
	S := OverflowScroll
	for _, tc := range []struct {
		name    string
		w       *Workspace
		other   *Workspace // nil: the drag stays in w
		id      WindowID
		a       Anchor
		want    c
		wantSrc c
		valid   bool
	}{
		{"stack above, lone", cols(S, 4, 0, []WindowID{1}, []WindowID{2}), nil, 1, win(2, domain.DropSideTop, domain.DropStack), c{{1, 2}}, nil, true},
		{"stack below, stacked", cols(S, 4, 0, []WindowID{1, 3}, []WindowID{2}), nil, 3, win(2, domain.DropSideBottom, domain.DropStack), c{{1}, {2, 3}}, nil, true},
		{"stack above the next one", cols(S, 4, 0, []WindowID{1, 2}), nil, 1, win(2, domain.DropSideTop, domain.DropStack), c{{1, 2}}, nil, false},
		{"stack below the previous one", cols(S, 4, 0, []WindowID{1, 2}), nil, 2, win(1, domain.DropSideBottom, domain.DropStack), c{{1, 2}}, nil, false},
		{"stack on itself", cols(S, 4, 0, []WindowID{1}, []WindowID{2}), nil, 1, win(1, domain.DropSideTop, domain.DropStack), c{{1}, {2}}, nil, false},
		{"stack other workspace", cols(S, 4, 0, []WindowID{1}), cols(S, 4, 0, []WindowID{9}), 9, win(1, domain.DropSideBottom, domain.DropStack), c{{1, 9}}, c{}, true},
		{"column right, lone", cols(S, 4, 0, []WindowID{1}, []WindowID{2}, []WindowID{3}), nil, 1, win(3, domain.DropSideRight, domain.DropColumn), c{{2}, {3}, {1}}, nil, true},
		{"column right of the next, lone", cols(S, 4, 0, []WindowID{1}, []WindowID{2}, []WindowID{3}), nil, 1, win(2, domain.DropSideRight, domain.DropColumn), c{{2}, {1}, {3}}, nil, true},
		{"column left of the next, lone", cols(S, 4, 0, []WindowID{1}, []WindowID{2}), nil, 1, win(2, domain.DropSideLeft, domain.DropColumn), c{{1}, {2}}, nil, false},
		{"column right of itself", cols(S, 4, 0, []WindowID{1}, []WindowID{2}), nil, 1, win(1, domain.DropSideRight, domain.DropColumn), c{{1}, {2}}, nil, false},
		{"column left, stacked", cols(S, 4, 0, []WindowID{1, 2}, []WindowID{3}), nil, 2, win(3, domain.DropSideLeft, domain.DropColumn), c{{1}, {2}, {3}}, nil, true},
		{"column left of own column, stacked", cols(S, 4, 0, []WindowID{1, 2}), nil, 2, win(1, domain.DropSideLeft, domain.DropColumn), c{{2}, {1}}, nil, true},
		{"column other workspace", cols(S, 4, 0, []WindowID{1}, []WindowID{2}), cols(S, 4, 0, []WindowID{9}), 9, win(1, domain.DropSideLeft, domain.DropColumn), c{{9}, {1}, {2}}, c{}, true},
		{"column full fixed, foreign", cols(OverflowFixed, 2, 0, []WindowID{1}, []WindowID{2}), cols(S, 4, 0, []WindowID{9}), 9, win(1, domain.DropSideLeft, domain.DropColumn), c{{1}, {2}}, c{{9}}, false},
		{"column full fixed, own lone", cols(OverflowFixed, 2, 0, []WindowID{1}, []WindowID{2}), nil, 1, win(2, domain.DropSideRight, domain.DropColumn), c{{2}, {1}}, nil, true},
		{"column full fixed, stacked", cols(OverflowFixed, 2, 0, []WindowID{1, 3}, []WindowID{2}), nil, 3, win(2, domain.DropSideRight, domain.DropColumn), c{{1, 3}, {2}}, nil, false},
		{"gap, lone", cols(S, 4, 0, []WindowID{1}, []WindowID{2}, []WindowID{3}), nil, 1, Anchor{Kind: AnchorGap, At: 3, Action: domain.DropColumn}, c{{2}, {3}, {1}}, nil, true},
		{"gap next to itself", cols(S, 4, 0, []WindowID{1}, []WindowID{2}), nil, 1, Anchor{Kind: AnchorGap, At: 1, Action: domain.DropColumn}, c{{1}, {2}}, nil, false},
		{"swap, lone", cols(S, 4, 0, []WindowID{1}, []WindowID{2}, []WindowID{3}), nil, 1, win(3, domain.DropSideCenter, domain.DropSwap), c{{3}, {2}, {1}}, nil, true},
		{"swap in own column", cols(S, 4, 0, []WindowID{1, 2}), nil, 1, win(2, domain.DropSideCenter, domain.DropSwap), c{{1, 2}}, nil, false},
		{"swap, stacked source", cols(S, 4, 0, []WindowID{1, 2}, []WindowID{3}), nil, 2, win(3, domain.DropSideCenter, domain.DropSwap), c{{1, 3}, {2}}, nil, true},
		{"swap lone onto a stacked tile: columns exchange", cols(S, 4, 0, []WindowID{1}, []WindowID{2, 3}), nil, 1, win(3, domain.DropSideCenter, domain.DropSwap), c{{2, 3}, {1}}, nil, true},
		{"swap other workspace", cols(S, 4, 0, []WindowID{1}, []WindowID{2}), cols(S, 4, 0, []WindowID{9}), 9, win(2, domain.DropSideCenter, domain.DropSwap), c{{1}, {9}}, c{{2}}, true},
		{"swap full fixed", cols(OverflowFixed, 2, 0, []WindowID{1}, []WindowID{2}), cols(S, 4, 0, []WindowID{9}), 9, win(2, domain.DropSideCenter, domain.DropSwap), c{{1}, {9}}, c{{2}}, true},
		{"none", cols(S, 4, 0, []WindowID{1}, []WindowID{2}), nil, 1, win(2, domain.DropSideCenter, domain.DropNone), c{{1}, {2}}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				src := tc.other
				if src == nil {
					src = tc.w
				}
				if got := tc.w.DropValid(src, tc.id, tc.a); got != tc.valid {
					t.Fatalf("valid = %v", got)
				}
				Drop(src, tc.w, tc.id, tc.a, 0, 0)
				if got := ids(tc.w); !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("columns %v, want %v", got, tc.want)
				}
				if tc.other != nil {
					got := ids(src)
					if got == nil {
						got = c{}
					}
					if !reflect.DeepEqual(got, tc.wantSrc) {
						t.Fatalf("source %v, want %v", got, tc.wantSrc)
					}
				}
				if tc.valid {
					if f, _ := tc.w.Focused(); f != tc.id {
						t.Fatalf("focus %d, want %d", f, tc.id)
					}
				}
			})
		})
	}
}

func TestDropBelow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := cols(OverflowScroll, 4, 0, []WindowID{1}, []WindowID{2})
		DropBelow(w, w, 1, 2)
		if got := ids(w); !reflect.DeepEqual(got, [][]WindowID{{2, 1}}) {
			t.Fatal(got)
		}
	})
}
