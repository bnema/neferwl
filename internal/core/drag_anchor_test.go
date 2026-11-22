package core_test

import (
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// dragTile drags tile from the centre of from to the point x, y and drops
// it there, returning the settled scene.
func dragTile(t *testing.T, r *dragRig, from domain.Rect, x, y int, msec uint32) []domain.Scene {
	t.Helper()
	r.moveTo(float64(from.X+from.W/2), float64(from.Y+from.H/2), msec)
	r.settle(t)
	r.press(t, 0x110, domain.ModSuper)
	r.moveTo(float64(x), float64(y), msec+1)
	r.settle(t)
	r.lands = 0
	r.in(ports.PointerButton{Button: 0x110})
	r.in(ports.KeyEvent{Keysym: "Super_L"})
	return r.settle(t)
}

func dragThreeColumns(t *testing.T, r *dragRig) []domain.Scene {
	t.Helper()
	for id := domain.WindowID(1); id <= 3; id++ {
		r.client <- ports.WindowMapped{ID: id}
		r.settle(t)
	}
	set := r.settle(t)
	if got := columnsOf(set, "A"); !slices.EqualFunc(got, [][]domain.WindowID{{1}, {2}, {3}}, slices.Equal) {
		t.Fatal(got)
	}
	return set
}

func wantColumns(t *testing.T, set []domain.Scene, want [][]domain.WindowID) {
	t.Helper()
	if got := columnsOf(set, "A"); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("columns %v, want %v", got, want)
	}
}

func TestDragSideBandsInsertColumns(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		r := startDragMode(t, animated, func(c *domain.Config) { c.Layout.MaxColumns = 3 }, dragOut)
		set := dragThreeColumns(t, r)
		// 3 on the left band of 1: a new first column.
		one, three := dragRect(t, set, "A", 1), dragRect(t, set, "A", 3)
		set = dragTile(t, r, three, one.X+40, one.Y+one.H/2, 1)
		wantColumns(t, set, [][]domain.WindowID{{3}, {1}, {2}})
		// 3 on the right band of 2: after 2.
		three, two := dragRect(t, set, "A", 3), dragRect(t, set, "A", 2)
		set = dragTile(t, r, three, two.X+two.W-40, two.Y+two.H/2, 3)
		wantColumns(t, set, [][]domain.WindowID{{1}, {2}, {3}})
	})
}

func TestDragSideNoneWidensCenter(t *testing.T) {
	both(t, func(t *testing.T, animated bool) {
		r := startDragMode(t, animated, func(c *domain.Config) {
			c.Layout.MaxColumns = 3
			c.Drop.SetWindow(domain.DropSideLeft, domain.DropNone)
			c.Drop.SetWindow(domain.DropSideCenter, domain.DropSwap)
		}, dragOut)
		set := dragThreeColumns(t, r)
		// The left band of 1 is the centre now: lone 3 swaps with 1.
		one, three := dragRect(t, set, "A", 1), dragRect(t, set, "A", 3)
		set = dragTile(t, r, three, one.X+40, one.Y+one.H/2, 1)
		wantColumns(t, set, [][]domain.WindowID{{3}, {2}, {1}})
	})
}

func TestDragEdgeReleaseActions(t *testing.T) {
	edge := func(side domain.DropSide, a domain.DropAction) func(*domain.Config) {
		return func(c *domain.Config) {
			c.Layout.MaxColumns = 3
			c.Drop.SetEdge(side, a)
		}
	}
	t.Run("column", func(t *testing.T) {
		both(t, func(t *testing.T, animated bool) {
			r := startDragMode(t, animated, edge(domain.DropSideLeft, domain.DropColumn), dragOut)
			set := dragThreeColumns(t, r)
			set = dragTile(t, r, dragRect(t, set, "A", 3), 3, 300, 1)
			wantColumns(t, set, [][]domain.WindowID{{3}, {1}, {2}})
		})
	})
	t.Run("stash", func(t *testing.T) {
		both(t, func(t *testing.T, animated bool) {
			r := startDragMode(t, animated, edge(domain.DropSideRight, domain.DropStash), dragOut)
			set := dragThreeColumns(t, r)
			set = dragTile(t, r, dragRect(t, set, "A", 2), 997, 300, 1)
			wantColumns(t, set, [][]domain.WindowID{{1}, {3}})
		})
	})
	t.Run("float", func(t *testing.T) {
		both(t, func(t *testing.T, animated bool) {
			r := startDragMode(t, animated, edge(domain.DropSideRight, domain.DropFloat), dragOut)
			set := dragThreeColumns(t, r)
			set = dragTile(t, r, dragRect(t, set, "A", 2), 997, 300, 1)
			w, ok := windowIn(set, "A", 2)
			if !ok || !w.Floating {
				t.Fatalf("2 is not a float: %+v", w)
			}
			// Centred on the pointer as far as the output allows.
			if w.Rect.X+w.Rect.W/2 != min(997, 1000-w.Rect.W/2) && w.Rect.X+w.Rect.W != 1000 {
				t.Fatalf("float %+v", w.Rect)
			}
			wantColumns(t, set, [][]domain.WindowID{{1}, {3}})
		})
	})
}

// A still pointer on the bottom edge of a scroll layout shows the next
// workspace; the drag survives and drops there.
func TestDragHoverSwitchesWorkspace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startDragRig(t, func(c *domain.Config) {
			c.Binds = map[string]string{"Super+s": "focus-workspace-next", "Super+a": "focus-workspace-prev"}
		}, dragOut)
		r.client <- ports.WindowMapped{ID: 1}
		r.client <- ports.WindowMapped{ID: 2}
		r.settle(t)
		// 3 on the second workspace, then back to the first.
		r.in(ports.KeyEvent{Keysym: "s", Mods: domain.ModSuper, Pressed: true})
		r.in(ports.KeyEvent{Keysym: "s", Mods: domain.ModSuper})
		r.settle(t)
		r.client <- ports.WindowMapped{ID: 3}
		r.settle(t)
		r.in(ports.KeyEvent{Keysym: "a", Mods: domain.ModSuper, Pressed: true})
		r.in(ports.KeyEvent{Keysym: "a", Mods: domain.ModSuper})
		set := r.settle(t)
		wantColumns(t, set, [][]domain.WindowID{{1}, {2}})
		one := dragRect(t, set, "A", 1)
		r.moveTo(float64(one.X+one.W/2), float64(one.Y+one.H/2), 1)
		r.settle(t)
		r.press(t, 0x110, domain.ModSuper)
		r.moveTo(500, 597, 2)
		r.settle(t)
		time.Sleep(399 * time.Millisecond)
		wantColumns(t, r.settle(t), [][]domain.WindowID{{1}, {2}})
		time.Sleep(time.Millisecond)
		set = r.settle(t)
		// The second workspace shows; 1 is still in its slot on the first,
		// out of the scene.
		wantColumns(t, set, [][]domain.WindowID{{3}})
		// Leave the edge before the next step; the drag is alive: a release
		// on the left band of 3 drops 1 there.
		three := dragRect(t, set, "A", 3)
		r.moveTo(float64(three.X+60), float64(three.Y+three.H/2), 3)
		r.settle(t)
		time.Sleep(time.Second)
		r.in(ports.PointerButton{Button: 0x110})
		r.in(ports.KeyEvent{Keysym: "Super_L"})
		set = r.settle(t)
		wantColumns(t, set, [][]domain.WindowID{{1}, {3}})
		r.in(ports.KeyEvent{Keysym: "a", Mods: domain.ModSuper, Pressed: true})
		r.in(ports.KeyEvent{Keysym: "a", Mods: domain.ModSuper})
		wantColumns(t, r.settle(t), [][]domain.WindowID{{2}})
	})
}

// In cascade the top and bottom edges scroll a band.
func TestDragHoverCascadeStepsBand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startDragRig(t, func(c *domain.Config) {
			c.Layout.Overflow = "cascade"
			c.Layout.MaxColumns = 1
		}, dragOut)
		for id := domain.WindowID(1); id <= 3; id++ {
			r.client <- ports.WindowMapped{ID: id}
			r.settle(t)
		}
		// Back to the first band.
		for range 2 {
			r.in(ports.KeyEvent{Keysym: "Up", Mods: domain.ModSuper, Pressed: true})
			r.in(ports.KeyEvent{Keysym: "Up", Mods: domain.ModSuper})
		}
		set := r.settle(t)
		one := dragRect(t, set, "A", 1)
		r.moveTo(float64(one.X+one.W/2), float64(one.Y+one.H/2), 1)
		r.settle(t)
		r.press(t, 0x110, domain.ModSuper)
		r.moveTo(500, 597, 2)
		r.settle(t)
		time.Sleep(400 * time.Millisecond)
		// 1 left the view (a thumbnail now): 2's band shows.
		set = r.settle(t)
		if w, _ := windowIn(set, "A", 1); !w.Proxy {
			t.Fatalf("1 still drawn: %+v", w)
		}
		if got := dragRect(t, set, "A", 2); got.Y != one.Y {
			t.Fatalf("no band step: 2 at %+v, band at %+v", got, one)
		}
		r.in(ports.KeyEvent{Keysym: "Escape", Mods: domain.ModSuper, Pressed: true})
		r.settle(t)
	})
}

// A named workspace has no neighbours: its workspace edge offers no zone
// and a rest there switches nothing.
func TestDragNamedWorkspaceHasNoWorkspaceEdge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startDragRig(t, func(c *domain.Config) {
			c.Binds = map[string]string{"Super+c": "workspace c"}
			c.Workspaces = []domain.WorkspaceConfig{{Name: "c"}}
		}, dragOut)
		r.in(ports.KeyEvent{Keysym: "c", Mods: domain.ModSuper, Pressed: true})
		r.in(ports.KeyEvent{Keysym: "c", Mods: domain.ModSuper})
		r.settle(t)
		r.client <- ports.WindowMapped{ID: 1}
		set := r.settle(t)
		one := dragRect(t, set, "A", 1)
		r.moveTo(float64(one.X+one.W/2), float64(one.Y+one.H/2), 1)
		r.settle(t)
		r.press(t, 0x110, domain.ModSuper)
		r.moveTo(500, 597, 2)
		set = r.settle(t)
		if p := dragScene(set, "A").DropPreview; p != nil && p.Label == domain.DropWorkspace.Label() {
			t.Fatalf("workspace zone on a named workspace: %+v", p)
		}
		time.Sleep(time.Second)
		if _, ok := windowIn(r.settle(t), "A", 1); !ok {
			t.Fatal("the named workspace was switched away")
		}
		r.in(ports.KeyEvent{Keysym: "Escape", Mods: domain.ModSuper, Pressed: true})
		r.settle(t)
	})
}

// The first workspace has none before it: its top edge (scroll layout)
// offers no workspace zone, while the bottom one does.
func TestDragFirstWorkspaceHasNoBackEdge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := startDragRig(t, nil, dragOut)
		r.client <- ports.WindowMapped{ID: 1}
		set := r.settle(t)
		one := dragRect(t, set, "A", 1)
		r.moveTo(float64(one.X+one.W/2), float64(one.Y+one.H/2), 1)
		r.settle(t)
		r.press(t, 0x110, domain.ModSuper)
		r.moveTo(500, 3, 2)
		if p := dragScene(r.settle(t), "A").DropPreview; p != nil && p.Label == domain.DropWorkspace.Label() {
			t.Fatalf("workspace zone before the first workspace: %+v", p)
		}
		r.moveTo(500, 597, 3)
		if p := dragScene(r.settle(t), "A").DropPreview; p == nil || p.Label != domain.DropWorkspace.Label() {
			t.Fatalf("no workspace zone toward the next one: %+v", p)
		}
		r.in(ports.KeyEvent{Keysym: "Escape", Mods: domain.ModSuper, Pressed: true})
		r.settle(t)
	})
}
