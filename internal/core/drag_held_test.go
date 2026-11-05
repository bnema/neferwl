package core

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
)

// A held fullscreen member is not draggable, like the cover.
func TestDraggableHeld(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := lineageCore(t)
		c.addScreen(domain.OutputInfo{Name: "OUT-1", Width: 300, Height: 200})
		w := c.cur().mon.Current()
		w.SetOverflow(OverflowScroll)
		w.AddWindow(1)
		w.AddWindow(2)
		w.FocusID(1)
		if _, _, ok := c.draggable(2); !ok {
			t.Fatal("plain tile not draggable")
		}
		w.Held = []WindowID{2}
		if _, _, ok := c.draggable(2); ok {
			t.Fatal("held member draggable")
		}
		if _, _, ok := c.draggable(1); !ok {
			t.Fatal("plain tile not draggable beside a held one")
		}
	})
}
