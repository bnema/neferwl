package workspace

import (
	"testing"
	"testing/synctest"
)

func TestFloatingWindows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := workspace()
		w.AddWindow(1)
		w.AddFloating(2, 20, 10)
		if id, _ := w.Focused(); id != 2 {
			t.Fatalf("focus %d, want the floating window", id)
		}
		l := w.Layout()
		f := l[len(l)-1]
		u := w.Usable
		if f.ID != 2 || !f.Floating || !f.Focused || f.Rect.W != 20 || f.Rect.H != 10 ||
			f.Rect.X != u.X+(u.W-20)/2 || f.Rect.Y != u.Y+(u.H-10)/2 {
			t.Fatalf("float placement %+v in %+v", f, u)
		}
		w.ResizeFloating(2, 500, 500)
		if r := w.Layout()[1].Rect; r.W != u.W || r.H != u.H {
			t.Fatalf("clamped %+v", r)
		}
		// A dialog from a fullscreen window waits hidden: the fullscreen is
		// exclusive and keeps the focus. It shows, focused, once it leaves.
		w.SetFullscreen(1, true)
		w.RemoveWindow(2)
		w.AddFloating(2, 20, 10)
		if l := w.Layout(); !l[1].Hidden || l[1].Focused || !l[0].Focused {
			t.Fatalf("float over fullscreen %+v", l)
		}
		w.SetFullscreen(1, false)
		if l := w.Layout(); l[1].Hidden || !l[1].Focused {
			t.Fatalf("float after fullscreen %+v", l)
		}
		w.RemoveWindow(2)
		if id, _ := w.Focused(); id != 1 || len(w.Floats) != 0 {
			t.Fatalf("focus after close %d", id)
		}

	})
}
