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
							if p.ID == 1 {
								if !p.Fullscreen {
									t.Fatal("original lost fullscreen placement")
								}
								if same && mode == OverflowCascade {
									if p.Rect.Overlaps(w.Output) {
										t.Fatalf("previous fullscreen bleeds into arrival viewport: %+v", p)
									}
								} else if !same || mode == OverflowFixed {
									if p.Rect != w.Output {
										t.Fatalf("cover rect %+v, want %+v", p.Rect, w.Output)
									}
								}
							}
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
						for _, p := range w.Layout() {
							if p.ID == 1 && p.Rect != w.Output {
								t.Fatalf("returned cover %+v, want %+v", p.Rect, w.Output)
							}
						}
					})
				})
			}
		}
	}
}

func TestLayoutUnadoptableCoverArrival(t *testing.T) {
	for _, mode := range []Overflow{OverflowCascade, OverflowScroll} {
		for _, stash := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stash=%v", mode, stash), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					w := &Workspace{Lanes: layout.Lanes{Overflow: mode, MaxColumns: 3, Output: Rect{W: 1200, H: 900}, Usable: Rect{W: 1200, H: 840}}}
					w.AddWindow(9)
					if stash {
						w.AddWindow(1)
						w.ToggleWindowStash()
					} else {
						w.AddDialog(1, 9, 400, 300)
					}
					w.SetFullscreen(1, true)
					if w.Cover() != 1 {
						t.Fatal("fixture has no cover")
					}
					w.AddAppWindow(2, 0, 400, 300)
					if !w.IsFloat(1) || !w.IsFloat(2) || w.Fullscreen != 1 {
						t.Fatal("cover or overlay changed ownership")
					}
					if id, _ := w.Focused(); id != 2 {
						t.Fatal("arrival not focused")
					}
					for _, p := range w.Layout() {
						if p.ID == 1 && p.Rect != w.Output {
							t.Fatalf("cover: %+v", p)
						}
						if p.ID == 2 && (p.Hidden || !p.Rect.Overlaps(w.Output)) {
							t.Fatalf("arrival: %+v", p)
						}
					}
				})
			})
		}
	}
}
