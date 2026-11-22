package core_test

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// grabTile presses Super + left on the centre of rect from.
func grabTile(t *testing.T, r *dragRig, from domain.Rect, msec uint32) {
	t.Helper()
	r.moveTo(float64(from.X+from.W/2), float64(from.Y+from.H/2), msec)
	r.settle(t)
	r.press(t, 0x110, domain.ModSuper)
}

// fourColumnsAtStart maps 1-4 and focuses 1: 3 and 4 are off screen to
// the right, so the right edge has room to scroll.
func fourColumnsAtStart(t *testing.T, r *dragRig) domain.Rect {
	t.Helper()
	for id := domain.WindowID(1); id <= 4; id++ {
		r.client <- ports.WindowMapped{ID: id}
		r.settle(t)
	}
	for range 3 {
		r.in(ports.KeyEvent{Keysym: "Left", Mods: domain.ModSuper, Pressed: true})
		r.in(ports.KeyEvent{Keysym: "Left", Mods: domain.ModSuper})
	}
	return dragRect(t, r.settle(t), "A", 1)
}

// Resting on the bottom edge reaches the empty trailing workspace; its
// side edges have nothing to scroll and a rest there does nothing.
func TestDragHoverEmptyWorkspaceSideEdge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startDragRig(t, func(c *domain.Config) {
			c.Binds = map[string]string{"Super+s": "focus-workspace-next", "Super+a": "focus-workspace-prev"}
		}, dragOut)
		// 1 on the first workspace, 2 on the second; a third one is empty.
		r.client <- ports.WindowMapped{ID: 1}
		r.settle(t)
		r.in(ports.KeyEvent{Keysym: "s", Mods: domain.ModSuper, Pressed: true})
		r.in(ports.KeyEvent{Keysym: "s", Mods: domain.ModSuper})
		r.settle(t)
		r.client <- ports.WindowMapped{ID: 2}
		r.settle(t)
		r.in(ports.KeyEvent{Keysym: "a", Mods: domain.ModSuper, Pressed: true})
		r.in(ports.KeyEvent{Keysym: "a", Mods: domain.ModSuper})
		one := dragRect(t, r.settle(t), "A", 1)
		grabTile(t, r, one, 1)
		r.moveTo(500, 597, 2)
		r.settle(t)
		// The second workspace at 400 ms, the empty third at 650 ms.
		time.Sleep(400 * time.Millisecond)
		if _, ok := windowIn(r.settle(t), "A", 2); !ok {
			t.Fatal("not on the second workspace")
		}
		time.Sleep(250 * time.Millisecond)
		if got := columnsOf(r.settle(t), "A"); len(got) != 0 {
			t.Fatalf("not on the empty workspace: %v", got)
		}
		// Let the switch's position hint expire: its hide publishes.
		time.Sleep(time.Second)
		r.settle(t)
		// Past the empty zone's hysteresis: only an edge could answer.
		r.moveTo(1, 300, 3)
		set := r.settle(t)
		if p := dragScene(set, "A").DropPreview; p != nil && p.Label == domain.DropScroll.Label() {
			t.Fatalf("scroll zone on an empty workspace: %+v", p)
		}
		// Nothing happens on the rest: no scene, the same workspace.
		select {
		case <-r.scenes:
		default:
		}
		time.Sleep(time.Second)
		synctest.Wait()
		select {
		case <-r.scenes:
			t.Fatal("published on a rest with nothing to do")
		default:
		}
		after := r.settle(t)
		if got := columnsOf(after, "A"); len(got) != 0 {
			t.Fatalf("columns %v on the empty workspace", got)
		}
		if _, ok := windowIn(after, "A", 2); ok {
			t.Fatal("switched away from the empty workspace")
		}
		r.in(ports.KeyEvent{Keysym: "Escape", Mods: domain.ModSuper, Pressed: true})
		r.settle(t)
	})
}

// A reload under a still pointer retargets the drag: the preview never
// shows a removed zone.
func TestDragReloadRetargetsPreview(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startDragRig(t, func(c *domain.Config) { c.Layout.MaxColumns = 3 }, dragOut)
		set := dragThreeColumns(t, r)
		one, three := dragRect(t, set, "A", 1), dragRect(t, set, "A", 3)
		grabTile(t, r, three, 1)
		r.moveTo(float64(one.X+40), float64(one.Y+one.H/2), 2)
		if p := dragScene(r.settle(t), "A").DropPreview; p == nil || p.Label != domain.DropColumn.Label() {
			t.Fatalf("no column zone: %+v", p)
		}
		cfg := r.cfg
		cfg.Layout.MaxColumns = 3
		cfg.Drop.SetWindow(domain.DropSideLeft, domain.DropNone)
		cfg.Drop.SetWindow(domain.DropSideCenter, domain.DropSwap)
		send(r.reload, domain.ConfigChanged{Config: cfg})
		if p := dragScene(r.settle(t), "A").DropPreview; p == nil || p.Label != domain.DropSwap.Label() {
			t.Fatalf("preview after the reload: %+v", p)
		}
		r.in(ports.KeyEvent{Keysym: "Escape", Mods: domain.ModSuper, Pressed: true})
		r.settle(t)
	})
}

// The release lands on the zone the preview shows, hysteresis included.
func TestDragReleaseKeepsPreviewZone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startDragRig(t, func(c *domain.Config) { c.Layout.MaxColumns = 3 }, dragOut)
		set := dragThreeColumns(t, r)
		one, three := dragRect(t, set, "A", 1), dragRect(t, set, "A", 3)
		grabTile(t, r, three, 1)
		r.moveTo(float64(one.X+40), float64(one.Y+one.H/2), 2)
		p := dragScene(r.settle(t), "A").DropPreview
		if p == nil || p.Label != domain.DropColumn.Label() {
			t.Fatalf("no column zone: %+v", p)
		}
		// One px past the band: still the column zone by hysteresis.
		bx := min(min(max(one.W/4, 48), 160), one.W/3)
		r.moveTo(float64(one.X+bx), float64(one.Y+one.H/2), 3)
		if q := dragScene(r.settle(t), "A").DropPreview; q == nil || q.Label != domain.DropColumn.Label() {
			t.Fatalf("zone left by 1 px: %+v", q)
		}
		r.in(ports.PointerButton{Button: 0x110})
		r.in(ports.KeyEvent{Keysym: "Super_L"})
		wantColumns(t, r.settle(t), [][]domain.WindowID{{3}, {1}, {2}})
	})
}

// At the end of the scroll range the hover timer stops: no step, no
// scene.
func TestDragHoverStopsAtScrollEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startDragRig(t, nil, dragOut)
		one := fourColumnsAtStart(t, r)
		grabTile(t, r, one, 1)
		r.moveTo(995, 300, 2)
		r.settle(t)
		// Two steps reach the last column.
		time.Sleep(400*time.Millisecond + 2*250*time.Millisecond)
		end := dragRect(t, r.settle(t), "A", 1)
		time.Sleep(250 * time.Millisecond)
		synctest.Wait()
		select {
		case set := <-r.scenes:
			t.Fatalf("published at the end of the range: 1 at %+v", dragRect(t, set, "A", 1))
		default:
		}
		if got := dragRect(t, r.settle(t), "A", 1); got != end {
			t.Fatalf("scrolled past the end: %+v from %+v", got, end)
		}
		r.in(ports.KeyEvent{Keysym: "Escape", Mods: domain.ModSuper, Pressed: true})
		r.settle(t)
	})
}

// A reload that removes the edge before the timer fires: nothing scrolls.
func TestDragHoverReloadRemovesEdge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startDragRig(t, nil, dragOut)
		one := fourColumnsAtStart(t, r)
		grabTile(t, r, one, 1)
		r.moveTo(995, 300, 2)
		r.settle(t)
		cfg := r.cfg
		cfg.Drop.SetEdge(domain.DropSideRight, domain.DropNone)
		send(r.reload, domain.ConfigChanged{Config: cfg})
		r.settle(t)
		time.Sleep(time.Second)
		if got := dragRect(t, r.settle(t), "A", 1); got != one {
			t.Fatalf("scrolled on a removed edge: %+v from %+v", got, one)
		}
		r.in(ports.KeyEvent{Keysym: "Escape", Mods: domain.ModSuper, Pressed: true})
		r.settle(t)
	})
}

// Ending the drag on a hover edge stops its timer: release, Escape, lock.
func TestDragHoverStopsWithDrag(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(r *dragRig)
	}{
		{"release", func(r *dragRig) {
			r.in(ports.PointerButton{Button: 0x110})
			r.in(ports.KeyEvent{Keysym: "Super_L"})
		}},
		{"escape", func(r *dragRig) {
			r.in(ports.KeyEvent{Keysym: "Escape", Mods: domain.ModSuper, Pressed: true})
		}},
		{"lock", func(r *dragRig) {
			locked := domain.SecurityState{Generation: 1, Protected: true}
			r.epoch.Store(locked)
			send(r.client, ports.ClientEvent(ports.SessionLockChanged{State: locked}))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := startDragRig(t, nil, dragOut)
				one := fourColumnsAtStart(t, r)
				grabTile(t, r, one, 1)
				r.moveTo(995, 300, 2)
				r.settle(t)
				tc.end(r)
				r.settle(t)
				if tc.name == "lock" {
					unlocked := domain.SecurityState{Generation: 2}
					r.epoch.Store(unlocked)
					send(r.client, ports.ClientEvent(ports.SessionLockChanged{State: unlocked}))
				}
				time.Sleep(time.Second)
				if got := dragRect(t, r.settle(t), "A", 1); got != one {
					t.Fatalf("scrolled after the drag ended: %+v from %+v", got, one)
				}
			})
		})
	}
}

// A dragged column scrolled out of the view gets a thumbnail and exactly
// one proxy of it.
func TestDragScrolledOutTileHasProxy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startDragRig(t, nil, dragOut)
		one := fourColumnsAtStart(t, r)
		grabTile(t, r, one, 1)
		r.moveTo(995, 300, 2)
		r.settle(t)
		time.Sleep(400*time.Millisecond + 2*250*time.Millisecond)
		s := dragScene(r.settle(t), "A")
		if p := s.DropPreview; p == nil || p.Thumb != 1 {
			t.Fatalf("no thumbnail: %+v", p)
		}
		n := 0
		for _, w := range s.Windows {
			if w.ID == 1 {
				n++
				if !w.Proxy {
					t.Errorf("1 drawn as %+v", w)
				}
			}
		}
		if n != 1 {
			t.Fatalf("%d entries of 1", n)
		}
		r.in(ports.KeyEvent{Keysym: "Escape", Mods: domain.ModSuper, Pressed: true})
		r.settle(t)
	})
}
