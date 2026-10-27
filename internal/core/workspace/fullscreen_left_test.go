package workspace

import (
	"testing"
	"testing/synctest"
)

func TestLeftFullscreenColumnMoved(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := cols(OverflowScroll, 2, 0, []WindowID{2, 3}, []WindowID{1})
		w.FocusID(2)
		w.Fullscreen = 2
		w.Activate(3)
		if w.left != 2 {
			t.Fatalf("latch %d", w.left)
		}
		if _, ok := w.takeColumn(); !ok || w.left != 0 {
			t.Fatalf("left %d", w.left)
		}

	})
}
