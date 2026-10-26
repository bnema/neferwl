package core

import (
	"math"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// overviewAt opens the overview on numbered workspace start of the standard
// rig (workspace 0 holds windows 1 and 2, workspace 1 window 3, workspace 2
// is the trailing empty one), every workspace using overflow.
func overviewAt(t *testing.T, overflow Overflow, start int) (*Core, *indicatorClock, *screen) {
	t.Helper()
	c, ic, sc := overviewRig(t)
	m := sc.mon
	m.Focus(start)
	for _, w := range m.Workspaces {
		w.Overflow = overflow
	}
	settleShown(t, c)
	toggleOverview(c, sc, ic.now)
	frame(t, c, ic, 5*time.Second)
	return c, ic, sc
}

// switchAndCheck runs act as one action from the open overview and checks
// that the overview axis is the landing workspace's workspace axis, and that
// every card of the workspace it lands on starts want away from its slot
// along that axis, and nothing across it.
func switchAndCheck(t *testing.T, c *Core, ic *indicatorClock, sc *screen, act func(), sign float64) {
	t.Helper()
	m := sc.mon
	old := m.Current()
	shots := c.snapshot(ic.now)
	act()
	if m.Current() == old {
		t.Fatal("fixture did not change workspace")
	}
	c.transition(shots, ic.now)
	indicatorScene(t, c)
	_, axis := m.Current().layout().axes()
	if m.overviewAxis() != axis {
		t.Fatalf("overview axis %v, want the landing layout's workspace axis %v", m.overviewAxis(), axis)
	}
	area := m.Current().overviewArea()
	travel := float64(area.H) * overviewWorkspaceTravel
	horizontal := axis == horizontalAxis
	if horizontal {
		travel = float64(area.W) * overviewWorkspaceTravel
	}
	count := 0
	for _, p := range sc.settledLayout {
		if p.Hidden || !m.Current().has(p.ID) {
			continue
		}
		count++
		rm := sc.rects[p.ID]
		got, cross := rm.dy, rm.dx
		if horizontal {
			got, cross = rm.dx, rm.dy
		}
		if got != sign*travel || cross != 0 {
			t.Errorf("tile %d offset (%g,%g), want %g along the axis (horizontal %v) and 0 across", p.ID, rm.dx, rm.dy, sign*travel, horizontal)
		}
	}
	if count == 0 {
		t.Fatal("no entering tile")
	}
}

// orderScenario is one workspace switch from the open overview. setup builds
// the fixture on the standard rig (workspace 0 holds windows 1 and 2,
// workspace 1 window 3, workspace 2 is the trailing empty one) and returns
// the action to run, the sign of the slide the entering cards must start
// with, and a check of what the fixture promised once the switch is done.
type orderScenario struct {
	name  string
	start int // numbered workspace the overview opens on (setup may move on)
	setup func(t *testing.T, m *Monitor) (act func(), sign float64, after func(t *testing.T))
}

func namedOrderMonitor(m *Monitor, names ...string) {
	specs := make([]NamedWorkspace, len(names))
	for i, n := range names {
		specs[i] = NamedWorkspace{Name: n}
	}
	m.SetNamed(specs)
}

func droppedFromRows(t *testing.T, m *Monitor, w *Workspace) {
	t.Helper()
	if indexOf(m.overviewWorkspaces(), w) >= 0 {
		t.Fatal("fixture: the left workspace is still an overview row")
	}
}

func orderScenarios() []orderScenario {
	sc := []orderScenario{
		// The workspace left may no longer be an overview row (an empty
		// workspace that is not current is skipped): the slide still comes
		// from the side it sat on.
		{"trailing-dropped", 2, func(t *testing.T, m *Monitor) (func(), float64, func(*testing.T)) {
			if !m.Workspaces[2].empty() {
				t.Fatal("fixture: workspace 2 is not the trailing empty one")
			}
			left := m.Workspaces[2]
			return func() { m.Focus(1) }, -1, func(t *testing.T) { droppedFromRows(t, m, left) }
		}},
		{"forward", 0, func(t *testing.T, m *Monitor) (func(), float64, func(*testing.T)) {
			return func() { m.Focus(1) }, 1, nil
		}},
		// Numbered and named rows are separated by a wider gap and a divider;
		// the slide has the same size and follows the same order.
		{"numbered-to-named", 0, func(t *testing.T, m *Monitor) (func(), float64, func(*testing.T)) {
			namedOrderMonitor(m, "dev")
			m.ToggleNamed("dev")
			m.AddWindow(30)
			m.ToggleNamed("dev")
			if dev := m.byName("dev"); !m.isHidden(dev) || dev.empty() {
				t.Fatal("fixture: dev is not an occupied named workspace")
			}
			return func() { m.ToggleNamed("dev") }, 1, nil
		}},
		{"named-to-numbered", 0, func(t *testing.T, m *Monitor) (func(), float64, func(*testing.T)) {
			namedOrderMonitor(m, "dev")
			m.ToggleNamed("dev")
			m.AddWindow(30)
			return func() { m.Focus(0) }, -1, nil
		}},
	}
	// A named workspace emptied while on screen drops out of the rows once
	// left; it sits below its anchor.
	for _, tc := range []struct {
		name string
		to   int
		sign float64
	}{{"named-dropped-to-its-anchor", 0, -1}, {"named-dropped-to-the-next-numbered", 1, 1}} {
		sc = append(sc, orderScenario{tc.name, 0, func(t *testing.T, m *Monitor) (func(), float64, func(*testing.T)) {
			namedOrderMonitor(m, "dev")
			m.ToggleNamed("dev")
			m.AddWindow(30)
			m.RemoveWindow(30)
			dev := m.Current()
			if !m.isHidden(dev) || !dev.empty() || dev.overviewAfter != m.Workspaces[0] {
				t.Fatalf("fixture: dev hidden %v empty %v anchor %v", m.isHidden(dev), dev.empty(), dev.overviewAfter)
			}
			return func() { m.Focus(tc.to) }, tc.sign, func(t *testing.T) { droppedFromRows(t, m, dev) }
		}})
	}
	// Two named workspaces under one anchor are ordered by configuration: the
	// rank term keeps them apart, also once the one left was emptied and dropped.
	for _, tc := range []struct {
		name      string
		from, to  string
		emptyFrom bool
		sign      float64
	}{
		{"same-anchor-dev-to-ops", "dev", "ops", false, 1},
		{"same-anchor-ops-to-dev", "ops", "dev", false, -1},
		{"same-anchor-dropped-dev-to-ops", "dev", "ops", true, 1},
		{"same-anchor-dropped-ops-to-dev", "ops", "dev", true, -1},
	} {
		sc = append(sc, orderScenario{tc.name, 0, func(t *testing.T, m *Monitor) (func(), float64, func(*testing.T)) {
			namedOrderMonitor(m, "dev", "ops")
			m.ToggleNamed("dev")
			m.AddWindow(30)
			m.ToggleNamed("ops")
			m.AddWindow(31)
			if tc.from == "dev" {
				m.ToggleNamed("dev")
			}
			dev, ops := m.byName("dev"), m.byName("ops")
			if dev.overviewAfter != m.Workspaces[0] || ops.overviewAfter != m.Workspaces[0] {
				t.Fatalf("fixture: anchors %v %v, want workspace 0", dev.overviewAfter, ops.overviewAfter)
			}
			from := m.Current()
			if from != m.byName(tc.from) {
				t.Fatal("fixture: not on the source workspace")
			}
			if tc.emptyFrom {
				m.RemoveWindow(map[string]WindowID{"dev": 30, "ops": 31}[tc.from])
				if !from.empty() {
					t.Fatal("fixture: source is not empty")
				}
			}
			return func() { m.ToggleNamed(tc.to) }, tc.sign, func(t *testing.T) {
				if tc.emptyFrom {
					droppedFromRows(t, m, from)
				}
			}
		}})
	}
	// A named workspace without a usable anchor (none, or one no longer in
	// the list) sits below the last occupied numbered row (workspace 1 of the
	// rig), else below row 0. Rows at or above it are entered from the left,
	// every row the rig can enter being at or above that position.
	for _, anchor := range []string{"nil", "removed"} {
		for _, to := range []int{0, 1} {
			sc = append(sc, orderScenario{"stale-" + anchor + "-anchor-to-" + string(rune('0'+to)), 0, func(t *testing.T, m *Monitor) (func(), float64, func(*testing.T)) {
				namedOrderMonitor(m, "dev")
				m.ToggleNamed("dev")
				m.AddWindow(30)
				m.RemoveWindow(30)
				dev := m.Current()
				dev.overviewAfter = nil
				if anchor == "removed" {
					dev.overviewAfter = &Workspace{}
				}
				at := 0
				for i, w := range m.Workspaces {
					if !w.empty() {
						at = i
					}
				}
				if at != 1 || !dev.empty() {
					t.Fatalf("fixture: fallback row %d, dev empty %v", at, dev.empty())
				}
				sign := 1.0
				if to <= at {
					sign = -1 // dev sits at at+0.5: after the target
				}
				return func() { m.Focus(to) }, sign, func(t *testing.T) { droppedFromRows(t, m, dev) }
			}})
		}
	}
	// A window moved to the workspace the action lands on is an entering card
	// of that workspace like the others, and the workspace it left is dropped.
	sc = append(sc, orderScenario{"moved-window", 1, func(t *testing.T, m *Monitor) (func(), float64, func(*testing.T)) {
		m.SetFollowMove(true)
		return func() { m.MoveToWorkspace(0, false) }, -1, func(t *testing.T) {
			if !m.Current().has(3) {
				t.Fatal("window 3 did not follow")
			}
		}
	}})
	return sc
}

// Every workspace switch from the open overview slides the landing
// workspace's cards in from the side it sits on relative to the one left,
// in every layout, whether or not the left workspace is still an overview row.
func TestOverviewWorkspaceSwitchOrder(t *testing.T) {
	for _, l := range registeredLayouts() {
		for _, s := range orderScenarios() {
			t.Run(string(l.name())+"/"+s.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					c, ic, sc := overviewRig(t)
					m := sc.mon
					m.Focus(s.start)
					act, sign, after := s.setup(t, m)
					for w := range m.all() {
						w.Overflow = l.name()
					}
					settleShown(t, c)
					toggleOverview(c, sc, ic.now)
					frame(t, c, ic, 5*time.Second)
					if s.name == "numbered-to-named" || s.name == "named-to-numbered" {
						if _, dividers := m.overviewRows(); len(dividers) == 0 {
							t.Fatal("fixture: no divider between numbered and named rows")
						}
					}
					switchAndCheck(t, c, ic, sc, act, sign)
					if after != nil {
						after(t)
					}
					if s.name == "moved-window" {
						if rm, ok := sc.rects[3]; !ok || rm.leaving {
							t.Fatalf("window 3 motion %+v (%v)", rm, ok)
						}
						for id, rm := range sc.rects {
							if rm.leaving {
								t.Errorf("leftover leaving entry %d", id)
							}
						}
						frame(t, c, ic, 5*time.Second)
						if len(sc.rects) != 0 {
							t.Fatal("motions did not settle")
						}
					}

				})
			})
		}
	}
}

// overviewOrder places a workspace among the overview rows with no animation:
// a numbered one at its index, a named one below its anchor by configured
// rank (staying under the next numbered row), and one with a stale anchor
// below the last occupied numbered row, else the first.
func TestMonitorOverviewOrder(t *testing.T) {
	occupied := func() *Workspace { return &Workspace{Columns: []Column{{Windows: []WindowID{1}}}} }
	list := []*Workspace{occupied(), occupied(), {}}
	m := &Monitor{named: []NamedWorkspace{{Name: "dev"}, {Name: "ops"}}}
	named := func(name string, anchor *Workspace) *Workspace {
		return &Workspace{Name: name, overviewAfter: anchor}
	}
	dev, ops := named("dev", list[0]), named("ops", list[0])
	anchored1 := named("dev", list[1])
	nilAnchor, removed := named("dev", nil), named("dev", &Workspace{})
	other := named("other", list[0]) // not configured: ranked after every configured one
	const eps = 1e-9
	for _, tc := range []struct {
		name string
		w    *Workspace
		want float64
	}{
		{"numbered", list[1], 1},
		{"trailing numbered", list[2], 2},
		{"named under the first", dev, 0.5},
		{"second rank sorts after the first", ops, 0.5 + 0.4/3},
		{"unconfigured sorts last", other, 0.5 + 0.4*2/3},
		{"named under the second", anchored1, 1.5},
		{"nil anchor: last occupied row", nilAnchor, 1.5},
		{"removed anchor: last occupied row", removed, 1.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				if got := m.overviewOrder(list, tc.w); got < tc.want-eps || got > tc.want+eps {
					t.Fatalf("order %g, want %g", got, tc.want)
				}

			})
		})
	}
	// Strictly between its anchor and the next numbered row, ranks included.
	if !(m.overviewOrder(list, dev) < m.overviewOrder(list, ops) && m.overviewOrder(list, ops) < m.overviewOrder(list, other) && m.overviewOrder(list, other) < 1) {
		t.Fatal("named rows must stay ordered under their anchor and above the next numbered row")
	}
	// With nothing occupied the fallback is the first row.
	empty := []*Workspace{{}, {}}
	if got := m.overviewOrder(empty, nilAnchor); got < 0.5-eps || got > 0.5+eps {
		t.Fatalf("no occupied row: order %g, want 0.5", got)
	}
}

// Closing the overview while the switch slide runs drops it: nothing of the
// preview stays, nor any card leaving.
func TestOverviewWorkspaceCloseMidSwitch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ic, sc := overviewAt(t, OverflowCascade, 1)
		m := sc.mon
		old := m.Current()
		shots := c.snapshot(ic.now)
		m.Focus(0)
		c.transition(shots, ic.now)
		frame(t, c, ic, 30*time.Millisecond)
		if len(sc.rects) == 0 {
			t.Fatal("fixture: no slide running")
		}
		toggleOverview(c, sc, ic.now)
		if m.ov.open || m.previewing() {
			t.Fatal("overview still open")
		}
		scene := indicatorScene(t, c)
		for id, rm := range sc.rects {
			if rm.leaving || old.has(id) {
				t.Errorf("window %d keeps a preview motion: %+v", id, rm)
			}
			if math.Abs(rm.dx) > 0 && !m.Current().has(id) {
				t.Errorf("window %d slides but is not on the current workspace", id)
			}
		}
		for _, w := range scene.Windows {
			if !w.Hidden && old.has(WindowID(w.ID)) {
				t.Errorf("window %d of the left workspace is drawn", w.ID)
			}
		}
		frame(t, c, ic, 5*time.Second)
		if len(sc.rects) != 0 || c.animating() {
			t.Fatalf("motions left after settling: %d, animating %v", len(sc.rects), c.animating())
		}

	})
}

// A veil or size change running on a card is not reset by the switch that
// enters its workspace: the group start carries it over like the per-window
// path does.
func TestOverviewWorkspaceSwitchKeepsRunningDimAndSize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ic, sc := overviewAt(t, OverflowCascade, 0)
		m := sc.mon
		rm := sc.rects[3]
		if !m.Workspaces[1].has(3) {
			t.Fatal("fixture: window 3 is not on workspace 1")
		}
		rm.dim, rm.ddim = c.spring(levelSpring(0.4, 0), ic.now), 0.4
		rm.w, rm.dw = c.spring(viewSpring(10, 0), ic.now), 10
		rm.h, rm.dh = c.spring(viewSpring(6, 0), ic.now), 6
		rm.scale = true
		if sc.rects == nil {
			sc.rects = make(map[WindowID]rectMotion)
		}
		sc.rects[3], sc.rectsWS = rm, sc.rectsOwner()
		ic.now = ic.now.Add(10 * time.Millisecond)
		shots := c.snapshot(ic.now)
		var shot rectShot
		for _, r := range shots[0].rects {
			if r.id == 3 {
				shot = r
			}
		}
		if shot.dim == 0 || shot.off.w == 0 || shot.off.h == 0 {
			t.Fatalf("fixture: shot %+v carries no running dim or size", shot)
		}
		m.Focus(1)
		c.transition(shots, ic.now)
		got := sc.rects[3]
		if got.ddim != shot.dim || !got.dim.on {
			t.Errorf("dim reset: %+v, want offset %g running", got.ddim, shot.dim)
		}
		if got.dw != shot.off.w || got.dh != shot.off.h || !got.w.on || !got.h.on || !got.scale {
			t.Errorf("size reset: (%g,%g) running %v/%v, want (%g,%g)", got.dw, got.dh, got.w.on, got.h.on, shot.off.w, shot.off.h)
		}

	})
}

// A workspace moved here from another screen while this one shows its
// overview is not a workspace switch of this screen: its cards get no group
// slide from the side of the workspace this screen left.
func TestOverviewWorkspaceMovedFromOtherScreenGetsNoGroupSlide(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, ic, a := overviewRig(t)
		c.addScreen(ports.OutputInfo{Name: "B", Width: 300, Height: 200})
		b := c.screens[1]
		a.mon.Focus(0)
		for w := range a.mon.all() {
			w.Overflow = OverflowCascade
		}
		for w := range b.mon.all() {
			w.Overflow = OverflowCascade
		}
		b.mon.AddWindow(10)
		b.mon.Focus(1)
		settleShown(t, c)
		toggleOverview(c, b, ic.now)
		frame(t, c, ic, 5*time.Second)
		if !b.mon.ov.open || a.mon.previewing() {
			t.Fatal("fixture: B must show its overview and A not")
		}
		moved := a.mon.Current()
		c.focusScreen = 0
		shots := c.snapshot(ic.now)
		c.moveWorkspace(dirRight)
		if b.mon.Current() != moved || !b.mon.ov.open {
			t.Fatal("fixture: workspace did not move to B's overview")
		}
		c.transition(shots, ic.now)
		indicatorScene(t, c)
		for _, p := range b.settledLayout {
			if p.Hidden || !moved.has(p.ID) {
				continue
			}
			if rm := b.rects[p.ID]; rm.dx != 0 || rm.dy != 0 || rm.x.on || rm.y.on {
				t.Errorf("moved window %d slides in as a group: %+v", p.ID, rm)
			}
		}

	})
}
