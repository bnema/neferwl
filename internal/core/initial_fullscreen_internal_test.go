package core

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/ports"
)

func TestInitialFullscreenPlacement(t *testing.T) {
	for _, floating := range []bool{false, true} {
		t.Run(map[bool]string{false: "tile", true: "float"}[floating], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, _, _ := fadeCore(t)
				w := c.cur().mon.Current()
				w.Overflow, w.MaxColumns = OverflowCascade, 3
				c.mapWindow(ports.WindowMapped{ID: 1})
				c.mapWindow(ports.WindowMapped{ID: 2, Fullscreen: true, Floating: floating})
				wantBands(t, w, []WindowID{1}, []WindowID{2})
				if id, _ := w.Focused(); id != 2 || w.Fullscreen != 2 || w.IsFloat(2) {
					t.Fatalf("focus=%d fullscreen=%d float=%v", id, w.Fullscreen, w.IsFloat(2))
				}
				c.mapWindow(ports.WindowMapped{ID: 3, Fullscreen: true, Floating: true, Parent: 2})
				if w.Fullscreen != 2 || !w.IsFloat(3) {
					t.Fatal("dialog replaced fullscreen parent")
				}
			})
		})
	}
}
