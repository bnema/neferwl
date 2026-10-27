package core

import (
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
)

// A full launcher must not copy the pending command on every unrelated event.
func TestPendingStartupReusesImmutableArguments(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := &Core{startup: [][]string{{"command", "argument"}}}
		c.ch.Spawn = make(chan domain.SpawnRequest, 1)
		c.ch.Spawn <- domain.SpawnRequest{}
		var req domain.SpawnRequest
		if allocations := testing.AllocsPerRun(100, func() { _, req = c.pendingStartup() }); allocations != 0 {
			t.Fatalf("pending startup: %.1f allocations", allocations)
		}
		if len(req.Argv) != 2 || &req.Argv[0] != &c.startup[0][0] {
			t.Fatal("pending arguments copied or changed")
		}

	})
}
