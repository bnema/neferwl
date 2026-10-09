package core

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// A full launcher must not copy the pending command on every unrelated event.
func TestPendingStartupReusesImmutableArguments(t *testing.T) {
	c := &Core{startup: [][]string{{"command", "argument"}}}
	c.ch.Spawn = make(chan ports.SpawnRequest, 1)
	c.ch.Spawn <- ports.SpawnRequest{}
	var req ports.SpawnRequest
	if allocations := testing.AllocsPerRun(100, func() { _, req = c.pendingStartup() }); allocations != 0 {
		t.Fatalf("pending startup: %.1f allocations", allocations)
	}
	if len(req.Argv) != 2 || &req.Argv[0] != &c.startup[0][0] {
		t.Fatal("pending arguments copied or changed")
	}
}
