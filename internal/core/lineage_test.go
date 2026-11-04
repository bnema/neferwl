package core

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func lineageNodes(pids ...int) []domain.ProcNode {
	nodes := make([]domain.ProcNode, len(pids))
	for i, pid := range pids {
		nodes[i] = domain.ProcNode{Key: domain.ProcKey{PID: pid, Start: 100}}
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
			first := c.cur().mon.Current()
			c.mapWindow(ports.WindowMapped{ID: 2, Proc: tc.b})
			first.RemoveWindow(2)
			c.cur().mon.Workspaces[1].AddWindow(2)
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
		child := lineageNodes(80, 20, 10)
		child[2].SessionLeader = true
		c.mapWindow(ports.WindowMapped{ID: 2, Proc: child})
		if c.launcherOf(2) != 1 {
			t.Fatal("first external window lost common non-leader B")
		}
	})
}
