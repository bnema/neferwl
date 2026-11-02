package workspace

import (
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core/layout"
)

// Core owns app identity; workspace receives only its same-app placement choice.
// Exercise that choice against each real engine, including a floating cover.
func TestLayoutCoveredArrivalConformance(t *testing.T) {
	for _, mode := range []Overflow{OverflowCascade, OverflowScroll, OverflowFixed} {
		for _, floating := range []bool{false, true} {
			for _, same := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/float=%v/same=%v", mode, floating, same), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						w := &Workspace{Lanes: layout.Lanes{Overflow: mode, MaxColumns: 3, Gaps: 8, Output: Rect{W: 1200, H: 900}, Usable: Rect{X: 20, Y: 40, W: 1160, H: 840}}}
						if floating {
							w.AddFloating(1, 400, 300)
						} else {
							w.AddWindow(1)
						}
						w.SetFullscreen(1, true)
						before := w.View
						if same {
							w.AddAppWindow(2, 1, 400, 300)
						} else {
							w.AddWindow(2)
						}
						if w.Fullscreen != 1 {
							t.Fatal("cover lost fullscreen state")
						}
						want := WindowID(1)
						if same {
							want = 2
						}
						if id, _ := w.Focused(); id != want {
							t.Fatalf("focus %d, want %d", id, want)
						}
						found := false
						for _, p := range w.Layout() {
							if p.ID != 2 {
								continue
							}
							found = true
							if p.Hidden == same || same && !p.Rect.Overlaps(w.Output) {
								t.Fatalf("arrival: %+v", p)
							}
						}
						if !found {
							t.Fatal("arrival missing")
						}
						if !same {
							if w.View != before || w.Cover() != 1 {
								t.Fatal("other app disturbed cover")
							}
							if floating && mode != OverflowCascade && !w.IsFloat(1) {
								t.Fatal("other app promoted cover")
							}
							return
						}
						if mode == OverflowFixed {
							if !w.IsFloat(2) || w.Cover() != 1 || w.IsFloat(1) != floating {
								t.Fatal("fixed did not preserve cover and overlay")
							}
							return
						}
						if w.IsFloat(1) || w.IsFloat(2) {
							t.Fatal("scrolling arrival did not tile cover and child")
						}
						if mode == OverflowCascade && w.Band(w.ColumnOf(2)) != w.Band(w.ColumnOf(1))+1 {
							t.Fatal("arrival not in next band")
						}
						if mode == OverflowScroll && w.ColumnOf(2) != w.ColumnOf(1)+1 {
							t.Fatal("arrival not right of cover")
						}
						// Returning to the original viewport restores its fullscreen cover.
						w.Focus = w.ColumnOf(1)
						w.scroll()
						if w.Cover() != 1 {
							t.Fatal("returning to fullscreen viewport lost cover")
						}
					})
				})
			}
		}
	}
}
