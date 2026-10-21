package core

import (
	"context"
	"fmt"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
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
			_ = w.snapPoints()
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

// BenchmarkBorderMotion is one pointer motion of a border resize with a
// publish; "same" repeats a position, so nothing changes after the first.
func BenchmarkBorderMotion(b *testing.B) {
	for _, mode := range []string{"moving", "same"} {
		b.Run(mode, func(b *testing.B) {
			var cfg ports.Config
			cfg.Keyboard.CmdKey = "super"
			cfg.Layout.MaxColumns = 3
			scenes, cmds := make(chan []ports.Scene, 1), make(chan ports.ClientCommand, 1024)
			c, err := New(cfg, Channels{Scenes: scenes, Commands: cmds}, Options{})
			if err != nil {
				b.Fatal(err)
			}
			c.addScreen(ports.OutputInfo{Name: "A", Width: 1920, Height: 1080})
			sc := c.cur()
			w := sc.mon.Current()
			w.Overflow = OverflowScroll
			for id := WindowID(1); id <= 12; id++ {
				w.AddWindow(id)
			}
			sc.arrange()
			ctx := context.Background()
			if err := c.publish(ctx); err != nil {
				b.Fatal(err)
			}
			rs := w.columnRects()
			g := newBorderResize(c, sc, w, border{kind: borderColumn, col: 0}, btnRight)
			x := float64(rs[0].X + rs[0].W)
			c.cursorY = 100
			n := 0
			b.ReportAllocs()
			for b.Loop() {
				c.cursorX = x
				if mode == "moving" {
					c.cursorX += float64(n%2*8) + 8
					n++
				}
				for len(scenes) > 0 {
					<-scenes
				}
				for len(cmds) > 0 {
					<-cmds
				}
				if err := g.motion(ctx, 0); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
