package core

import (
	"testing"
	"time"
)

// Trigger budget includes snapshot, transition, animate and refreshing the
// published layout between actions, but not scene construction.
// Measured baseline: workspace switch 6, selection move 7 allocations.
func TestOverviewWorkspaceSwitchAllocations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		group  bool
		budget int
		move   func(m *Monitor, to int)
	}{
		{"switch", true, 6, func(m *Monitor, to int) { m.Focus(to) }},
		{"selection", false, 7, func(m *Monitor, to int) { m.OverviewMove(0, 1-2*to) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, ic, sc := overviewAt(t, OverflowCascade, 0)
			m := sc.mon
			m.Current().MaxColumns = 1
			settleShown(t, c)
			to := 1
			step := func() {
				shots := c.snapshot(ic.now)
				tc.move(m, to)
				c.transition(shots, ic.now)
				ic.now = ic.now.Add(time.Millisecond)
				c.animate(ic.now, nil)
				c.refreshShown()
				to = 1 - to
			}
			step()
			step()
			if len(sc.rects) == 0 {
				t.Fatal("setup: no card motion")
			}
			// The switch takes the group path: its entering cards share
			// one offset along the axis.
			if rm := sc.rects[1]; tc.group && (!m.Current().has(1) || rm.dx == 0 || rm.dy != 0) {
				t.Fatalf("setup: window 1 has no group slide: %+v", rm)
			}
			n := testing.AllocsPerRun(50, step)
			t.Logf("%s allocs per run = %v", tc.name, n)
			if n > float64(tc.budget) {
				t.Errorf("%s allocs per run = %v, budget %d", tc.name, n, tc.budget)
			}
		})
	}
}

func TestOverviewWorkspaceAnimatedFrameAllocations(t *testing.T) {
	c, ic, sc := overviewRig(t)
	m := sc.mon
	m.Current().Overflow = OverflowCascade
	m.AddWindow(4)
	m.AddWindow(5)
	m.Focus(1)
	for id := WindowID(10); id < 13; id++ {
		m.AddWindow(id)
		m.Current().FocusID(id)
		m.Current().ToggleWindowStash()
	}
	m.Focus(0)
	settleShown(t, c)
	toggleOverview(c, sc, ic.now)
	frame(t, c, ic, 5*time.Second)
	shots := c.snapshot(ic.now)
	m.Focus(1)
	c.transition(shots, ic.now)
	frame(t, c, ic, 40*time.Millisecond)
	leaving := 0
	for _, rm := range sc.rects {
		if rm.leaving {
			leaving++
		}
	}
	if leaving == 0 || len(m.Workspaces[1].Stash) < 3 {
		t.Fatalf("fixture needs leaving cards and a stack: leaving %d, stash %d", leaving, len(m.Workspaces[1].Stash))
	}
	// Hold the controlled clock on an in-flight frame: AllocsPerRun must not
	// turn the fixture into a settled scene during its warmup or repetitions.
	n := testing.AllocsPerRun(50, func() { c.animate(ic.now, nil); indicatorScene(t, c) })
	// Measured baseline for refreshShown plus full scene publication:
	// 52 normally, 62 under -race (including the controlled clock mock).
	budget := 52.0
	if raceEnabled {
		budget = 62
	}
	t.Logf("animated frame allocs per run = %v", n)
	if n > budget {
		t.Errorf("animated frame allocations %v, budget %v", n, budget)
	}
}
