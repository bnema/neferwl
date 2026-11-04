package core

import (
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/ports"
)

func TestLineageObserverDormantAndAllocations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := lineageCore(t)
		m := c.cur().mon
		for i := 0; i < 256; i++ {
			m.Workspaces = append(m.Workspaces, m.newWorkspace())
		}
		c.observeLineageFocus()
		if c.lineage.workspaceFocus != nil || c.cur().lineageFocus.count != 0 {
			t.Fatal("dormant observer initialized")
		}
		if n := testing.AllocsPerRun(100, c.observeLineageFocus); n != 0 {
			t.Fatalf("dormant allocations %g", n)
		}
		c.mapLineage(ports.WindowMapped{ID: 1, Proc: lineageNodes(10)})
		if c.cur().lineageFocus.count == 0 {
			t.Fatal("first process record did not initialize history")
		}
		if n := testing.AllocsPerRun(100, c.observeLineageFocus); n != 0 {
			t.Fatalf("settled allocations %g", n)
		}
		c.removeLineage(1, true)
		generation := c.lineage.generation
		c.observeLineageFocus()
		if c.lineage.generation != generation || len(c.lineage.workspaceFocus) != 0 {
			t.Fatal("last process removal did not restore dormancy")
		}
	})
}

func BenchmarkLineageObserverSettled(b *testing.B) {
	for _, n := range []int{16, 256, 1024} {
		for _, active := range []bool{false, true} {
			b.Run(fmt.Sprintf("workspaces=%d/active=%v", n, active), func(b *testing.B) {
				c := publishRig(b, 1)
				m := c.cur().mon
				for i := 0; i < n; i++ {
					m.Workspaces = append(m.Workspaces, m.newWorkspace())
				}
				if active {
					c.mapLineage(ports.WindowMapped{ID: 999, Proc: lineageNodes(99)})
				}
				c.observeLineageFocus()
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					c.observeLineageFocus()
				}
			})
		}
	}
}
