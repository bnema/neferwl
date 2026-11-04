package core

import (
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func lineageNodes(pids ...int) []domain.ProcNode {
	nodes := make([]domain.ProcNode, len(pids))
	for i, pid := range pids {
		nodes[i] = domain.ProcNode{Started: time.Unix(1000, 0), Key: domain.ProcKey{PID: pid, Start: 100}}
	}
	return nodes
}

func lineageCore(t *testing.T) *Core {
	t.Helper()
	cfg := domain.Config{}
	cfg.Keyboard.CmdKey = "alt"
	cfg.Layout.MaxColumns = 1
	c, err := New(cfg, Channels{Scenes: make(chan []domain.Scene, 1)}, testOptions(t, Options{}))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLineageResolution(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		child, candidate             []domain.ProcNode
		childShared, candidateShared bool
		want                         WindowID
	}{
		{name: "direct crossing session leader", child: lineageNodes(60, 50, 40), candidate: lineageNodes(40), want: 1},
		{name: "Steam game and Big Picture", child: lineageNodes(60, 50, 40, 30, 20, 10), candidate: lineageNodes(70, 20, 10), want: 1},
		{name: "session leader meeting rejected", child: lineageNodes(60, 10), candidate: lineageNodes(70, 10)},
		{name: "starttime mismatch", child: lineageNodes(60, 40), candidate: lineageNodes(40)},
		{name: "shared child", child: lineageNodes(60, 40), candidate: lineageNodes(40), childShared: true},
		{name: "shared candidate", child: lineageNodes(60, 40), candidate: lineageNodes(40), candidateShared: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := lineageCore(t)
				switch tc.name {
				case "direct crossing session leader":
					tc.child[1].SessionLeader = true
				case "Steam game and Big Picture":
					tc.child[5].SessionLeader = true
					tc.candidate[0].SessionLeader = true
					tc.candidate[2].SessionLeader = true
				case "session leader meeting rejected":
					tc.child[1].SessionLeader = true
					tc.candidate[1].SessionLeader = true
				case "starttime mismatch":
					tc.child[1].Key.Start++
				}
				c.mapWindow(ports.WindowMapped{ID: 1, Proc: tc.candidate, PIDShared: tc.candidateShared})
				c.cur().lineageFocus = focusHistory{}
				c.cur().lineageFocus.observe(time.Unix(999, 0), 1)
				c.mapWindow(ports.WindowMapped{ID: 2, Proc: tc.child, PIDShared: tc.childShared})
				if got := c.launcherOf(2); got != tc.want {
					t.Fatalf("launcher %d want %d", got, tc.want)
				}
			})
		})
	}
}

func TestLineageLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := lineageCore(t)
		c.mapWindow(ports.WindowMapped{ID: 1, Proc: lineageNodes(10)})
		c.mapWindow(ports.WindowMapped{ID: 2, Proc: lineageNodes(20, 10)})
		c.mapWindow(ports.WindowMapped{ID: 3, Proc: lineageNodes(30, 20, 10)})
		if c.launcherOf(3) != 2 {
			t.Fatal("missing grandchild edge")
		}
		c.removeLineage(2, false)
		c.cur().mon.RemoveWindow(2)
		if c.launcherOf(3) != 1 || c.launcherOf(2) != 1 {
			t.Fatal("unmap did not reparent and retain own edge")
		}
		c.mapWindow(ports.WindowMapped{ID: 2, Remap: true, Proc: lineageNodes(999)})
		if c.launcherOf(2) != 1 || c.lineage.records[2].proc[0].Key.PID != 20 {
			t.Fatal("remap changed edge or identity")
		}
		c.removeLineage(2, true)
		if c.lineage.records[2] != nil || c.launcherOf(3) != 1 {
			t.Fatal("destroy lifecycle")
		}
		c.mapWindow(ports.WindowMapped{ID: 4, Floating: true, Parent: 1})
		if c.launcherOf(4) != 1 || c.lineage.records[4].edge != lineageDialog {
			t.Fatal("dialog edge")
		}
	})
}

func TestLineageRecencyAndFocusAllocations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := lineageCore(t)
		// Two Steam surfaces on distinct workspaces, each workspace-focused.
		c.mapWindow(ports.WindowMapped{ID: 1, Proc: lineageNodes(70, 20, 10)})
		first := c.cur().mon.Current()
		c.cur().mon.show(c.cur().mon.Workspaces[1])
		second := c.cur().mon.Current()
		c.mapWindow(ports.WindowMapped{ID: 2, Proc: lineageNodes(70, 20, 10)})
		// Move the second surface out of lineage placement to model Big Picture's tv workspace.
		first.RemoveWindow(2)
		second.AddWindow(2)
		c.observeLineageFocus()
		c.cur().mon.show(first)
		c.observeLineageFocus()
		c.cur().mon.show(second)
		c.observeLineageFocus()
		c.cur().lineageFocus = focusHistory{}
		c.cur().lineageFocus.observe(time.Unix(999, 0), 2)
		if got := c.resolveLauncher(ports.WindowMapped{ID: 3, Proc: lineageNodes(60, 50, 40, 30, 20, 10)}); got != 2 {
			t.Fatalf("recent Big Picture launcher: %d", got)
		}
		if got := testing.AllocsPerRun(100, func() { c.observeLineageFocus() }); got != 0 {
			t.Fatalf("focus observer allocates: %g", got)
		}
	})
}

func TestLineageMeetingOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, tc := range []struct {
			name        string
			a, b, child []domain.ProcNode
			want        WindowID
		}{
			{"direct before common", lineageNodes(80, 20), lineageNodes(10), lineageNodes(60, 20, 10), 2},
			{"nearest new node", lineageNodes(70, 20), lineageNodes(80, 30), lineageNodes(60, 20, 30), 1},
			{"nearest candidate node", lineageNodes(70, 90, 20), lineageNodes(80, 20), lineageNodes(60, 20), 2},
			{"latest mapped", lineageNodes(70, 20), lineageNodes(80, 20), lineageNodes(60, 20), 2},
		} {
			c := lineageCore(t)
			c.mapWindow(ports.WindowMapped{ID: 1, Proc: tc.a})
			c.cur().lineageFocus = focusHistory{}
			c.cur().lineageFocus.observe(time.Unix(999, 0), 1)
			first := c.cur().mon.Current()
			c.mapWindow(ports.WindowMapped{ID: 2, Proc: tc.b})
			first.RemoveWindow(2)
			c.cur().mon.Workspaces[1].AddWindow(2)
			c.cur().lineageFocus.observe(time.Unix(1000, 0), 2)
			if got := c.resolveLauncher(ports.WindowMapped{ID: 3, Proc: tc.child}); got != tc.want {
				t.Fatalf("%s: %d want %d", tc.name, got, tc.want)
			}
		}
	})
}

// The first external window must retain B even without an eligible peer:
// [P] cannot meet sibling [G,B,A], whereas [P,B,A] meets non-leader B.
func TestFirstExternalSiblingChain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := lineageCore(t)
		a := lineageNodes(70, 20, 10)
		a[2].SessionLeader = true
		c.mapWindow(ports.WindowMapped{ID: 1, Proc: a})
		c.cur().lineageFocus = focusHistory{}
		c.cur().lineageFocus.observe(time.Unix(999, 0), 1)
		child := lineageNodes(80, 20, 10)
		child[2].SessionLeader = true
		c.mapWindow(ports.WindowMapped{ID: 2, Proc: child})
		if c.launcherOf(2) != 1 {
			t.Fatal("first external window lost common non-leader B")
		}
	})
}

func TestRetainedChildReparentedOnLauncherRemoval(t *testing.T) {
	for _, destroyed := range []bool{false, true} {
		for _, grandparent := range []bool{false, true} {
			t.Run(fmt.Sprintf("destroyed=%v/grandparent=%v", destroyed, grandparent), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					c := lineageCore(t)
					var want WindowID
					if grandparent {
						c.mapWindow(ports.WindowMapped{ID: 1, Proc: lineageNodes(10)})
						want = 1
					}
					c.mapWindow(ports.WindowMapped{ID: 2, Proc: lineageNodes(20, 10)})
					c.mapWindow(ports.WindowMapped{ID: 3, Proc: lineageNodes(30, 20, 10)})
					c.removeLineage(3, false)
					c.cur().mon.RemoveWindow(3)
					c.removeLineage(2, destroyed)
					c.cur().mon.RemoveWindow(2)
					c.mapWindow(ports.WindowMapped{ID: 3, Remap: true})
					if got := c.launcherOf(3); got != want {
						t.Fatalf("retained launcher %d want %d", got, want)
					}
				})
			})
		}
	}
}

func TestLineageFocusWorkspaceChurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := lineageCore(t)
		m := c.cur().mon
		c.mapLineage(ports.WindowMapped{ID: 99, Proc: lineageNodes(99)})
		for i := 0; i < 100; i++ {
			w := m.Current()
			w.AddWindow(1)
			m.normalize()
			c.observeLineageFocus()
			next := m.Workspaces[len(m.Workspaces)-1]
			m.show(next)
			w.RemoveWindow(1)
			m.normalize()
			c.observeLineageFocus()
			if _, retained := c.lineage.workspaceFocus[w]; retained {
				t.Fatal("retired workspace retained")
			}
			if len(c.lineage.workspaceFocus) > len(m.Workspaces) {
				t.Fatalf("observer grew: %d entries, %d live workspaces", len(c.lineage.workspaceFocus), len(m.Workspaces))
			}
		}
		if n := testing.AllocsPerRun(100, func() { c.observeLineageFocus() }); n != 0 {
			t.Fatalf("settled observer allocates: %g", n)
		}
	})
}

func TestShellMeetingExclusionLive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := lineageCore(t)
		candidate := lineageNodes(10, 5)
		candidate[1].Comm = "bash"
		c.mapWindow(ports.WindowMapped{ID: 1, Proc: candidate})
		c.cur().lineageFocus = focusHistory{}
		c.cur().lineageFocus.observe(time.Unix(999, 0), 1)
		child := lineageNodes(20, 5)
		child[1].Comm = "bash"
		if got := c.resolveLauncher(ports.WindowMapped{ID: 2, Proc: child}); got != 0 {
			t.Fatal("shell established common lineage")
		}
		c.lineage.records[1].proc[1].Comm = "custom-wrapper"
		child[1].Comm = "custom-wrapper"
		if got := c.resolveLauncher(ports.WindowMapped{ID: 2, Proc: child}); got != 1 {
			t.Fatal("non-shell wrapper rejected before reload")
		}
		c.cfg.LineageShells = []string{"custom-wrapper"}
		if got := c.resolveLauncher(ports.WindowMapped{ID: 2, Proc: child}); got != 0 {
			t.Fatal("live shell extension ignored")
		}
		c.lineage.records[1].proc[0].Comm = "bash"
		if got := c.resolveLauncher(ports.WindowMapped{ID: 2, Proc: lineageNodes(20, 10)}); got != 1 {
			t.Fatal("direct ancestry changed")
		}
	})
}
