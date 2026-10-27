package hints

import (
	"cmp"
	"slices"

	"github.com/bnema/neferwl/internal/domain"
)

const (
	// dupIoU is the IoU above which two boxes are the same element: the
	// higher score is kept.
	dupIoU = 0.5
	// nestRatio is the share of the smaller box that must lie inside the
	// other for the two to be nested.
	nestRatio = 0.85
)

func area(r domain.Rect) int { return r.W * r.H }

// interArea is the area of the intersection of a and b.
func interArea(a, b domain.Rect) int {
	w := min(a.X+a.W, b.X+b.W) - max(a.X, b.X)
	h := min(a.Y+a.H, b.Y+b.H) - max(a.Y, b.Y)
	if w <= 0 || h <= 0 {
		return 0
	}
	return w * h
}

// nested reports whether the smaller of a and b lies for at least nestRatio
// inside the other.
func nested(a, b domain.Rect) bool {
	return float64(interArea(a, b)) >= nestRatio*float64(min(area(a), area(b)))
}

// mergeTargets removes the duplicate and nested boxes the detector returns
// for one element. It works on logical targets (after rounding), in two
// passes so that the result does not depend on the processing order:
//
//  1. duplicates: by descending score, a box whose IoU with an already kept
//     box is over dupIoU is dropped;
//  2. nesting: a box that contains another surviving box (that box lies
//     nestRatio inside it) is dropped, whatever the scores: the smallest of
//     a nested chain is kept (a row loses against its icon and its text).
//
// The result is ordered by descending score, then position.
func mergeTargets(in []domain.HintTarget) []domain.HintTarget {
	s := slices.Clone(in)
	slices.SortStableFunc(s, func(a, b domain.HintTarget) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(a.Rect.Y, b.Rect.Y), cmp.Compare(a.Rect.X, b.Rect.X),
			cmp.Compare(area(a.Rect), area(b.Rect)))
	})
	kept := s[:0]
	for _, t := range s {
		dup := false
		for _, k := range kept {
			inter := interArea(t.Rect, k.Rect)
			if inter > 0 && float64(inter) > dupIoU*float64(area(t.Rect)+area(k.Rect)-inter) {
				dup = true
				break
			}
		}
		if !dup {
			kept = append(kept, t)
		}
	}
	out := make([]domain.HintTarget, 0, len(kept))
	for i, t := range kept {
		contains := false
		for j, k := range kept {
			if i != j && area(k.Rect) < area(t.Rect) && nested(t.Rect, k.Rect) {
				contains = true
				break
			}
		}
		if !contains {
			out = append(out, t)
		}
	}
	return out
}
