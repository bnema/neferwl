package workspace

import (
	"fmt"
	"testing"

	"github.com/bnema/neferwl/internal/core/layout"
)

func benchLane(n int, stamped bool) *Workspace {
	w := spanWorkspace(OverflowScroll, n)
	if stamped {
		stampSpans(w, 0, n-1)
	}
	w.Focus = n / 2
	return w
}

func benchLanes(b *testing.B, f func(b *testing.B, w *Workspace)) {
	for _, n := range []int{16, 64} {
		for _, stamped := range []bool{false, true} {
			b.Run(fmt.Sprintf("cols=%d/stamped=%t", n, stamped), func(b *testing.B) {
				w := benchLane(n, stamped)
				b.ReportAllocs()
				f(b, w)
			})
		}
	}
}

func BenchmarkSnapPoints(b *testing.B) {
	benchLanes(b, func(b *testing.B, w *Workspace) {
		for b.Loop() {
			_ = w.Engine().SnapPoints(w.Snapshot(), nil)
		}
	})
}

func BenchmarkLayout(b *testing.B) {
	benchLanes(b, func(b *testing.B, w *Workspace) {
		for b.Loop() {
			_ = w.Layout()
		}
	})
}

func BenchmarkColumnRects(b *testing.B) {
	benchLanes(b, func(b *testing.B, w *Workspace) {
		var buf []Rect
		for b.Loop() {
			buf = w.columnRectsInto(buf, false)
		}
	})
}

func BenchmarkScroll(b *testing.B) {
	benchLanes(b, func(b *testing.B, w *Workspace) {
		for b.Loop() {
			w.scroll()
		}
	})
}

// hotWorkspace is n one-window columns on a 1920x1080 output, max 3 columns.
func hotWorkspace(o Overflow, n int) *Workspace {
	w := &Workspace{Lanes: layout.Lanes{Overflow: o, MaxColumns: 3, Usable: Rect{W: 1920, H: 1080}, Output: Rect{W: 1920, H: 1080}, Gaps: 8}}
	for i := 1; i <= n; i++ {
		w.AddWindow(WindowID(i))
	}
	w.Focus = n / 2
	w.scroll()
	return w
}

// BenchmarkLayoutColumnRects is the per-frame column geometry.
func BenchmarkLayoutColumnRects(b *testing.B) {
	for _, o := range []Overflow{OverflowScroll, OverflowFixed, OverflowCascade} {
		b.Run(string(o), func(b *testing.B) {
			w := hotWorkspace(o, 12)
			var buf []Rect
			b.ReportAllocs()
			for b.Loop() {
				buf = w.columnRectsInto(buf, false)
			}
		})
	}
}

// BenchmarkLayoutPreviewTiles is the overview projection of one workspace.
func BenchmarkLayoutPreviewTiles(b *testing.B) {
	for _, o := range []Overflow{OverflowScroll, OverflowCascade} {
		b.Run(string(o), func(b *testing.B) {
			w := hotWorkspace(o, 12)
			tiles, _, _ := w.previewTilesInto(nil)
			b.ReportAllocs()
			for b.Loop() {
				tiles, _, _ = w.previewTilesInto(tiles[:0])
			}
		})
	}
}

// BenchmarkLayoutBorderAt is a pointer hit test over the column borders.
func BenchmarkLayoutBorderAt(b *testing.B) {
	for _, o := range []Overflow{OverflowScroll, OverflowFixed, OverflowCascade} {
		b.Run(string(o), func(b *testing.B) {
			w := hotWorkspace(o, 12)
			// A point on no border: every column is examined.
			b.ReportAllocs()
			for b.Loop() {
				w.BorderAt(1, 1, 4)
			}
		})
	}
}
