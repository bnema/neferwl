package core

import (
	"context"
	"testing"
	"testing/synctest"
)

// A sample whose content did not change keeps the published preview: the
// same pointer, no allocation; a thumbnail move replaces it.
func TestAdvanceDropPreviewReusesUnchanged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := dragCore(t)
		w := c.cur().mon.Current()
		w.Columns = []Column{{Windows: []WindowID{1}}, {Windows: []WindowID{2}}}
		if err := c.beginDrag(context.Background(), 1, btnLeft, false, 0); err != nil {
			t.Fatal(err)
		}
		r, _ := modelPlaced(w, 2)
		c.cursorX, c.cursorY = float64(r.X+r.W/2), float64(r.Y+r.H/2)
		c.retarget(w)
		c.advanceDropPreview(c.now())
		p := c.drag.target.preview
		if p == nil || p.Label == "" {
			t.Fatalf("preview %+v", p)
		}
		now := c.now()
		if n := testing.AllocsPerRun(100, func() { c.advanceDropPreview(now) }); n != 0 {
			t.Fatalf("unchanged sample allocated %v times", n)
		}
		if c.drag.target.preview != p {
			t.Fatal("unchanged sample replaced the preview")
		}
		// A thumbnail that follows the pointer is a new preview.
		c.drag.anim.zoom = 0
		old := *p
		c.cursorX++
		c.advanceDropPreview(c.now())
		if c.drag.target.preview.Thumb == 0 && c.drag.target.preview != p {
			t.Fatal("no thumbnail, same zone: replaced")
		}
		if *p != old {
			t.Fatal("a published preview changed")
		}
	})
}
