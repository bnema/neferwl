package core

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// flashWorkspaces is flashCore with windows 1 to 3 on the first workspace and
// window 4 on the second, which is shown.
func flashWorkspaces(t *testing.T, overflow Overflow) (*Core, *indicatorClock) {
	t.Helper()
	c, clock := flashCore(t)
	m := c.cur().mon
	m.SetOverflow(overflow)
	c.windows.mapped(ports.WindowMapped{ID: 4}, clock.now.Add(-time.Minute))
	m.Focus(1)
	m.AddWindow(4)
	indicatorScene(t, c)
	if m.Current() != m.Workspaces[1] || c.flash.pending != nil || flashTimers(clock) != 0 {
		t.Fatal("setup: not on the second workspace, or a flash is already requested")
	}
	return c, clock
}

// flashTimers counts the flash requests so far: each starts one settle timer.
func flashTimers(clock *indicatorClock) int {
	n := 0
	for _, d := range clock.timers {
		if d == flashSettle {
			n++
		}
	}
	return n
}

// wantSwitchFlash expects exactly one pending window flash for the focused
// window of the current workspace.
func wantSwitchFlash(t *testing.T, c *Core, clock *indicatorClock) WindowID {
	t.Helper()
	id, _ := c.cur().mon.Focused()
	if id == 0 {
		t.Fatal("setup: the target workspace has no focused window")
	}
	if c.flash.pending == nil || *c.flash.pending != (flashTarget{level: flashWindow, window: id, output: "A"}) {
		t.Fatalf("pending %+v, want a window flash on %d", c.flash.pending, id)
	}
	if n := flashTimers(clock); n != 1 {
		t.Fatalf("%d flash requests, want one", n)
	}
	return id
}

func TestFlashWorkspaceSwitchByAction(t *testing.T) {
	// from and to are numbered workspace indexes; to holds the flashed window.
	for _, tc := range []struct {
		name     string
		overflow Overflow
		action   Action
		from, to int
		want     WindowID
	}{
		{"prev", OverflowScroll, ActionFocusWorkspacePrev, 1, 0, 3},
		{"next", OverflowScroll, ActionFocusWorkspaceNext, 0, 1, 4},
		{"number", OverflowScroll, "focus-workspace 1", 1, 0, 3},
		{"cascade prev", OverflowCascade, ActionFocusWorkspacePrev, 1, 0, 3},
		{"cascade next", OverflowCascade, ActionFocusWorkspaceNext, 0, 1, 4},
		{"cascade number", OverflowCascade, "focus-workspace 1", 1, 0, 3},
		// An edge move lands on the workspace: it flashes the window, never
		// the column the old layout diff would pick.
		{"edge to earlier workspace flashes the window, scroll window up", OverflowScroll, ActionFocusWindowUp, 1, 0, 3},
		{"edge to later workspace flashes the window, scroll window down", OverflowScroll, ActionFocusWindowDown, 0, 1, 4},
		{"edge to earlier workspace flashes the window, cascade column left", OverflowCascade, ActionFocusColumnLeft, 1, 0, 3},
		{"edge to later workspace flashes the window, cascade column right", OverflowCascade, ActionFocusColumnRight, 0, 1, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, clock := flashWorkspaces(t, tc.overflow)
			m := c.cur().mon
			if tc.from != 1 {
				m.Focus(tc.from)
				indicatorScene(t, c)
			}
			c.applyAction(tc.action)
			if m.Current() != m.Workspaces[tc.to] {
				t.Fatalf("action left workspace %d, want %d", m.Active, tc.to)
			}
			if id := wantSwitchFlash(t, c, clock); id != tc.want {
				t.Fatalf("flash on %d, want %d", id, tc.want)
			}
		})
	}
}

// workspace <name> shows the named workspace and flashes its focused window.
func TestFlashNamedWorkspaceBind(t *testing.T) {
	c, clock := flashWorkspaces(t, OverflowScroll)
	m := c.cur().mon
	specs := []NamedWorkspace{{Name: "dev"}}
	m.SetNamed(specs)
	c.specs = specs
	c.windows.mapped(ports.WindowMapped{ID: 5}, clock.now.Add(-time.Minute))
	numbered := m.Current()
	m.ToggleNamed("dev")
	m.AddWindow(5)
	m.ToggleNamed("dev")
	if m.Current() != numbered || m.byName("dev").empty() {
		t.Fatal("setup: named workspace not filled, or not left")
	}
	indicatorScene(t, c)
	c.applyAction("workspace dev")
	if m.Current() != m.byName("dev") {
		t.Fatal("bind did not show the named workspace")
	}
	if id := wantSwitchFlash(t, c, clock); id != 5 {
		t.Fatalf("flash on %d, want 5", id)
	}
}

func TestFlashWorkspaceSwitchToEmptyWorkspaceDoesNotFlash(t *testing.T) {
	c, clock := flashCore(t)
	m := c.cur().mon
	c.applyAction(ActionFocusWorkspaceNext)
	if m.Current() != m.Workspaces[1] {
		t.Fatal("did not switch")
	}
	if id, _ := m.Focused(); id != 0 {
		t.Fatalf("setup: workspace not empty (%d)", id)
	}
	if c.flash.pending != nil || flashTimers(clock) != 0 {
		t.Fatal("empty workspace flashed")
	}
}

func TestFlashWorkspaceSwitchRespectsConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Core)
	}{
		{"flash off", func(c *Core) { c.cfg.Focus.Flash = false }},
		{"animations off", func(c *Core) { c.cfg.Animations.On = false }},
		{"protected", func(c *Core) { c.security.Protected = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, clock := flashWorkspaces(t, OverflowScroll)
			tc.change(c)
			c.applyAction(ActionFocusWorkspacePrev)
			if c.cur().mon.Current() != c.cur().mon.Workspaces[0] {
				t.Fatal("did not switch")
			}
			if c.flash.pending != nil || flashTimers(clock) != 0 {
				t.Fatal("flashed despite the config")
			}
		})
	}
}

// With animations on the flash waits for the slide, then marks the focused
// window of the new workspace where it settled.
func TestFlashWorkspaceSwitchWaitsForSlide(t *testing.T) {
	c, clock := flashWorkspaces(t, OverflowScroll)
	sc := c.cur()
	act(c, clock, ActionFocusWorkspacePrev)
	id := wantSwitchFlash(t, c, clock)
	if !sc.mon.switchView.busy() {
		t.Fatal("setup: no workspace slide running")
	}
	clock.now = clock.now.Add(flashSettle)
	if c.flashTick() || c.flash.pending == nil {
		t.Fatal("flash started while the workspace slides")
	}
	if indicatorScene(t, c).Flash != nil {
		t.Fatal("flash drawn during the slide")
	}
	clock.now = clock.now.Add(10 * time.Second)
	c.animate(clock.now, nil)
	if !c.flashTick() {
		t.Fatal("flash did not start after the slide")
	}
	if c.flash.running.window != id {
		t.Fatalf("flash on %d, want %d", c.flash.running.window, id)
	}
	var want Rect
	for _, p := range sc.mon.Layout() {
		if p.ID == id {
			want = p.Rect
		}
	}
	if want == (Rect{}) || c.flash.rect != want {
		t.Fatalf("flash rect %+v, want the settled %+v", c.flash.rect, want)
	}
	f := indicatorScene(t, c).Flash
	if f == nil || f.Rect != want {
		t.Fatalf("scene flash %+v", f)
	}
}

// A second switch before the flash starts leaves one flash, on the last target.
func TestFlashWorkspaceSwitchRetargets(t *testing.T) {
	c, clock := flashWorkspaces(t, OverflowScroll)
	c.applyAction(ActionFocusWorkspacePrev)
	c.applyAction(ActionFocusWorkspaceNext)
	if id, _ := c.cur().mon.Focused(); id != 4 || c.flash.pending == nil || c.flash.pending.window != 4 {
		t.Fatalf("pending %+v", c.flash.pending)
	}
	clock.now = clock.now.Add(flashSettle)
	if !c.flashTick() || c.flash.running.window != 4 {
		t.Fatal("last switch did not win")
	}
}

type wsSwipe struct {
	c  *Core
	at time.Duration
}

func (s *wsSwipe) begin() {
	s.at += time.Second
	s.c.swipeBegin(ports.SwipeBegin{Fingers: 3, Time: s.at})
}

func (s *wsSwipe) move(n int, dy float64) {
	for range n {
		s.at += 8 * time.Millisecond
		s.c.swipeUpdate(ports.SwipeUpdate{DY: dy, Time: s.at})
	}
}

func (s *wsSwipe) end(cancelled bool) {
	s.c.swipeEnd(ports.SwipeEnd{Cancelled: cancelled, Time: s.at})
}

func TestFlashWorkspaceSwipeCommit(t *testing.T) {
	c, clock := flashWorkspaces(t, OverflowScroll)
	sc := c.cur()
	s := &wsSwipe{c: c}
	s.begin()
	s.move(10, -40)
	if g := c.swipe; g == nil || g.mode != swipeWorkspaces {
		t.Fatalf("swipe %+v, want a workspace swipe", g)
	}
	if c.flash.pending != nil {
		t.Fatal("flash requested before the swipe committed")
	}
	s.end(false)
	if sc.mon.Current() != sc.mon.Workspaces[0] {
		t.Fatal("swipe did not switch")
	}
	id := wantSwitchFlash(t, c, clock)
	clock.now = clock.now.Add(flashSettle)
	if c.flashTick() {
		t.Fatal("flash started while the landing slide runs")
	}
	clock.now = clock.now.Add(10 * time.Second)
	c.animate(clock.now, nil)
	if !c.flashTick() || c.flash.running.window != id {
		t.Fatal("flash did not start on the focused window after the slide")
	}
}

func TestFlashWorkspaceSwipeCancelDoesNotFlash(t *testing.T) {
	c, clock := flashWorkspaces(t, OverflowScroll)
	sc := c.cur()
	s := &wsSwipe{c: c}
	s.begin()
	s.move(10, -40)
	s.end(true)
	if sc.mon.Current() != sc.mon.Workspaces[1] {
		t.Fatal("cancelled swipe switched")
	}
	if c.flash.pending != nil || flashTimers(clock) != 0 {
		t.Fatal("cancelled swipe flashed")
	}
}

// A swipe that is not cancelled but lands where it began changes no workspace:
// nothing to flash.
func TestFlashWorkspaceSwipeSnapBackDoesNotFlash(t *testing.T) {
	c, clock := flashWorkspaces(t, OverflowScroll)
	sc := c.cur()
	s := &wsSwipe{c: c}
	s.begin()
	s.move(1, -20)
	if g := c.swipe; g == nil || g.mode != swipeWorkspaces {
		t.Fatalf("swipe %+v, want a workspace swipe", g)
	}
	s.at += time.Second // a slow release does not carry the swipe on
	s.end(false)
	if sc.mon.Current() != sc.mon.Workspaces[1] {
		t.Fatal("setup: the swipe left the workspace")
	}
	if c.flash.pending != nil || flashTimers(clock) != 0 {
		t.Fatal("snap back flashed")
	}
}

func TestFlashWorkspaceSwipeToEmptyWorkspaceDoesNotFlash(t *testing.T) {
	c, clock := flashCore(t)
	sc := c.cur()
	indicatorScene(t, c)
	s := &wsSwipe{c: c}
	s.begin()
	s.move(10, 40)
	s.end(false)
	if sc.mon.Current() != sc.mon.Workspaces[1] {
		t.Fatal("swipe did not reach the empty workspace")
	}
	if c.flash.pending != nil || flashTimers(clock) != 0 {
		t.Fatal("empty workspace flashed")
	}
}

func TestFlashWorkspaceSwipeRespectsConfig(t *testing.T) {
	c, clock := flashWorkspaces(t, OverflowScroll)
	c.cfg.Focus.Flash = false
	s := &wsSwipe{c: c}
	s.begin()
	s.move(10, -40)
	s.end(false)
	if c.cur().mon.Current() != c.cur().mon.Workspaces[0] {
		t.Fatal("swipe did not switch")
	}
	if c.flash.pending != nil || flashTimers(clock) != 0 {
		t.Fatal("flashed despite flash = off")
	}
}

// Picking a window in the overview already flashes it: the workspace change it
// causes must not add a second flash. Each case opens the overview on one
// workspace and lands on the other.
func TestFlashOverviewSelectFlashesOnce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start int // numbered workspace shown when the overview opens
		pick  func(*testing.T, *Core, ports.Scene)
		want  WindowID
	}{
		{"return key", 0, func(t *testing.T, c *Core, _ ports.Scene) {
			c.applyAction(ActionFocusWorkspaceNext)
			if err := c.keyEvent(context.Background(), ports.KeyEvent{Keysym: "Return", Pressed: true}); err != nil {
				t.Fatal(err)
			}
		}, 4},
		{"toggle bind", 0, func(t *testing.T, c *Core, _ ports.Scene) {
			c.applyAction(ActionFocusWorkspaceNext)
			c.applyAction(ActionToggleOverview)
		}, 4},
		{"click", 0, func(t *testing.T, c *Core, _ ports.Scene) {
			// The selection moves to the second workspace; its cards then
			// take the click.
			c.applyAction(ActionFocusWorkspaceNext)
			c.animate(c.now().Add(10*time.Second), nil)
			indicatorScene(t, c)
			for _, p := range c.cur().shownLayout() {
				if p.ID == 4 {
					c.cursorX, c.cursorY = float64(p.Rect.X+p.Rect.W/2), float64(p.Rect.Y+p.Rect.H/2)
				}
			}
			if picked, err := c.overviewClick(context.Background()); err != nil || !picked {
				t.Fatalf("click %v %v", picked, err)
			}
		}, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, clock := flashWorkspaces(t, OverflowScroll)
			m := c.cur().mon
			m.Focus(tc.start)
			c.applyAction(ActionToggleOverview)
			clock.now = clock.now.Add(10 * time.Second)
			c.animate(clock.now, nil)
			scene := indicatorScene(t, c)
			if !m.ov.open || c.flash.pending != nil || flashTimers(clock) != 0 {
				t.Fatal("setup: overview not open, or flashed on open")
			}
			tc.pick(t, c, scene)
			if m.ov.open || m.Current() != m.Workspaces[1] {
				t.Fatal("overview did not land on the other workspace")
			}
			if id := wantSwitchFlash(t, c, clock); id != tc.want {
				t.Fatalf("flash on %d, want %d", id, tc.want)
			}
		})
	}
}

// Workspace binds inside the overview move its selection and do not flash.
func TestFlashOverviewWorkspaceBindDoesNotFlash(t *testing.T) {
	c, clock := flashWorkspaces(t, OverflowScroll)
	c.applyAction(ActionToggleOverview)
	indicatorScene(t, c)
	c.applyAction(ActionFocusWorkspacePrev)
	if c.flash.pending != nil || flashTimers(clock) != 0 {
		t.Fatal("overview workspace step flashed")
	}
}

// A move that follows its window to another workspace shows that workspace:
// it flashes the moved window once. Without follow-move the view stays.
func TestFlashMoveToWorkspaceFollowsOnlyWithFollowMove(t *testing.T) {
	for _, follow := range []bool{false, true} {
		t.Run(fmt.Sprintf("follow-move %v", follow), func(t *testing.T) {
			c, clock := flashWorkspaces(t, OverflowScroll)
			m := c.cur().mon
			m.Focus(0)
			m.SetFollowMove(follow)
			c.applyAction(ActionMoveWindowToWorkspaceNext)
			if (m.Current() == m.Workspaces[1]) != follow {
				t.Fatalf("shown workspace %d", m.Active)
			}
			if !follow {
				if c.flash.pending != nil || flashTimers(clock) != 0 {
					t.Fatal("flashed without a workspace switch")
				}
				return
			}
			if id := wantSwitchFlash(t, c, clock); id != 3 {
				t.Fatalf("flash on %d, want the moved window 3", id)
			}
		})
	}
}
