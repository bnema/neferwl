package core

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
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
	})
}

// A flip of another output leaves the drag preview unsampled: its slide
// moves only with its own output's frames.
func TestDropPreviewIgnoresOtherFlip(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := publishRig(t, 2)
		ctx := context.Background()
		a, b := c.screens[c.screenIndex("A")], c.screens[c.screenIndex("B")]
		w := a.mon.Current()
		id := w.Columns[0].Windows[0]
		if err := c.beginDrag(ctx, id, btnLeft, false, 0); err != nil {
			t.Fatal(err)
		}
		r, _ := modelPlaced(w, w.Columns[1].Windows[0])
		c.cursorX, c.cursorY = float64(a.x+r.X+r.W/2), float64(a.y+r.Y+r.H/2)
		c.retarget(w)
		if err := c.publish(ctx); err != nil {
			t.Fatal(err)
		}
		// A new zone on A: its preview slides.
		c.cursorY = float64(a.y + r.Y + 2)
		if !c.retarget(w) || !c.drag.anim.sliding {
			t.Fatalf("no slide to %+v", c.drag.target.a)
		}
		p := c.drag.target.preview
		time.Sleep(10 * time.Millisecond)
		if err := c.publishFrame(ctx, b); err != nil {
			t.Fatal(err)
		}
		if c.drag.target.preview != p {
			t.Fatal("a flip of B resampled A's preview")
		}
		if err := c.publishFrame(ctx, a); err != nil {
			t.Fatal(err)
		}
		if c.drag.target.preview == p {
			t.Fatal("a flip of A left its sliding preview")
		}
	})
}

// A thumbnail of a tile scrolled out of view follows the pointer: each
// move is a new preview, and the published one never changes.
func TestDropThumbnailMoveReplacesPreview(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := dragCore(t)
		sc := c.cur()
		w := sc.mon.Current()
		w.Columns = []Column{{Windows: []WindowID{1}}, {Windows: []WindowID{2}}, {Windows: []WindowID{3}}, {Windows: []WindowID{4}}, {Windows: []WindowID{5}}}
		if err := c.beginDrag(context.Background(), 1, btnLeft, false, 0); err != nil {
			t.Fatal(err)
		}
		// Focus the last column: 1 scrolls out of view.
		w.FocusID(5)
		sc.arrange()
		c.refreshShown()
		c.cursorX, c.cursorY = 500, 300
		c.retarget(w)
		c.advanceDropPreview(c.now())
		p := c.drag.target.preview
		if p == nil || p.Thumb != 1 {
			t.Fatalf("no thumbnail: %+v", p)
		}
		old := *p
		c.cursorX += 10
		c.advanceDropPreview(c.now())
		q := c.drag.target.preview
		if q == p || q.ThumbRect.X != old.ThumbRect.X+10 {
			t.Fatalf("thumbnail did not move: %+v from %+v", q, old)
		}
		if *p != old {
			t.Fatal("a published preview changed")
		}
	})
}

// The thumbnail stays in the viewport frame the renderer clips to.
func TestDropThumbInFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := dragCore(t)
		sc := c.cur()
		w := sc.mon.Current()
		w.Columns = []Column{{Windows: []WindowID{1}}}
		frame := Rect{X: 200, Y: 100, W: 600, H: 400}
		w.Output = frame
		var buf []Placement
		for _, pt := range [][2]float64{{0, 0}, {999, 599}, {500, 300}} {
			c.cursorX, c.cursorY = pt[0], pt[1]
			r, zoom := c.dropThumb(sc, 1, &buf)
			if zoom <= 0 || !frame.Contains(r) {
				t.Errorf("cursor %v: thumbnail %+v outside %+v", pt, r, frame)
			}
		}
	})
}
