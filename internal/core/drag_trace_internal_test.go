package core

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
)

// dropTraces drains the window drops traced on ch.
func dropTraces(ch chan domain.InputTrace) []domain.WindowDropped {
	var got []domain.WindowDropped
	for len(ch) > 0 {
		if d, ok := (<-ch).(domain.WindowDropped); ok {
			got = append(got, d)
		}
	}
	return got
}

// releaseAt moves the drag to x, y and releases it.
func releaseAt(t *testing.T, c *Core, x, y float64) {
	t.Helper()
	_, w := c.screenOf(c.drag.id)
	c.cursorX, c.cursorY = x, y
	c.drag.moved = true
	c.retarget(w)
	if err := c.endDrag(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// center is the middle of window id on w, global.
func center(w *Workspace, id WindowID) (float64, float64) {
	r, _ := modelPlaced(w, id)
	return float64(r.X + r.W/2), float64(r.Y + r.H/2)
}

func TestWindowDropTraced(t *testing.T) {
	cases := []struct {
		name string
		// drag starts a drag and returns the release point.
		drag func(t *testing.T, c *Core) (float64, float64)
		want []domain.WindowDropped
	}{
		{"tab", func(t *testing.T, c *Core) (float64, float64) {
			return center(threeTileDrag(t, c), 3)
		}, []domain.WindowDropped{{Window: 1, Kind: "tab", Output: "OUT", OutputAfter: "OUT", WorkspaceBefore: 1, WorkspaceAfter: 1}}},
		{"other output", func(t *testing.T, c *Core) (float64, float64) {
			c.addScreen(domain.OutputInfo{Name: "B", Width: 1000, Height: 600})
			b := c.screens[c.screenIndex("B")]
			threeTileDrag(t, c)
			return float64(b.x + 500), float64(b.y + 300)
		}, []domain.WindowDropped{{Window: 1, Kind: "column", Output: "OUT", OutputAfter: "B", WorkspaceBefore: 1, WorkspaceAfter: 1}}},
		{"other workspace", func(t *testing.T, c *Core) (float64, float64) {
			m := c.cur().mon
			m.addWindow(1, 0)
			m.Focus(1)
			m.addWindow(2, 0)
			m.Focus(0)
			if err := c.beginDrag(context.Background(), 1, btnLeft, false, 0); err != nil {
				t.Fatal(err)
			}
			// What a hover on the workspace edge does.
			m.Focus(1)
			return center(m.Current(), 2)
		}, []domain.WindowDropped{{Window: 1, Kind: "tab", Output: "OUT", OutputAfter: "OUT", WorkspaceBefore: 1, WorkspaceAfter: 1}}},
		{"edge stash", func(t *testing.T, c *Core) (float64, float64) {
			c.cfg.Drop.SetEdge(domain.DropSideRight, domain.DropStash)
			threeTileDrag(t, c)
			return 995, 300
		}, []domain.WindowDropped{{Window: 1, Kind: "stash", Output: "OUT", OutputAfter: "OUT", WorkspaceBefore: 1, WorkspaceAfter: 1}}},
		{"invalid zone", func(t *testing.T, c *Core) (float64, float64) {
			w := threeTileDrag(t, c)
			c.drag.moved = false
			x, y := center(w, 1)
			c.cursorX, c.cursorY = x, y
			if err := c.endDrag(context.Background()); err != nil {
				t.Fatal(err)
			}
			threeTileDrag(t, c)
			c.cancelDrag()
			threeTileDrag(t, c)
			return 995, 300 // hover edge
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := dragCore(t)
				ch := make(chan domain.InputTrace, 16)
				c.ch.Binds = ch
				x, y := tc.drag(t, c)
				releaseAt(t, c, x, y)
				got := dropTraces(ch)
				if len(got) != len(tc.want) {
					t.Fatalf("traces %+v, want %+v", got, tc.want)
				}
				for i := range got {
					if got[i] != tc.want[i] {
						t.Fatalf("trace %+v, want %+v", got[i], tc.want[i])
					}
				}
			})
		})
	}
}

// With traces off a drop sends nothing and does not allocate a trace.
func TestWindowDropUntracedWithoutChannel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := dragCore(t)
		x, y := center(threeTileDrag(t, c), 3)
		releaseAt(t, c, x, y)
		if c.ch.Binds != nil {
			t.Fatal("binds channel set")
		}
		if m := c.markDrop(1, c.cur(), c.cur().mon.Current()); m.ok {
			t.Fatalf("mark %+v with traces off", m)
		}
	})
}
