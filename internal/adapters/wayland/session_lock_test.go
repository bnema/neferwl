package wayland

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/sessionsecurity"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
	lockclient "github.com/bnema/wlturbo/protocol/extsessionlock"
)

func lockManagerServer(t *testing.T) (*Server, *sessionsecurity.Gate, chan ports.SecurityState, chan ports.SecurityBackendEvent, string) {
	t.Helper()
	gate := &sessionsecurity.Gate{}
	changes := make(chan ports.SecurityState, 1)
	events := make(chan ports.SecurityBackendEvent, 16)
	dir := t.TempDir()
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs, Security: gate}, Channels{SecurityChanges: changes, SecurityEvents: events}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("session-lock server did not stop")
		}
	})
	return s, gate, changes, events, dir
}

func bindLockManager(t *testing.T, c *wlturbo.Display) *lockclient.ExtSessionLockManager {
	t.Helper()
	manager := lockclient.NewExtSessionLockManager(c.Context())
	g, ok := c.Registry().FindGlobal(lockclient.ExtSessionLockManagerInterface)
	if !ok {
		t.Fatal("missing test lock manager")
	}
	if err := c.Registry().Bind(g.Name, g.Interface, 1, manager); err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestSessionLockWaitsForEveryBarrierAndOwnerDeathStaysProtected(t *testing.T) {
	s, gate, changes, _, dir := lockManagerServer(t)
	c := protocolClient(t, s, dir)
	manager := bindLockManager(t, c)
	lock, err := manager.Lock()
	if err != nil {
		t.Fatal(err)
	}
	locked := 0
	lock.OnLocked(func() { locked++ })
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	var state ports.SecurityState
	select {
	case state = <-changes:
	case <-time.After(2 * time.Second):
		t.Fatal("no backend transition")
	}
	if !state.Protected || !gate.Snapshot().Protected || locked != 0 {
		t.Fatalf("early lock %+v events %d", state, locked)
	}
	if !s.display.Do(func() {
		s.applySecurityEvent(ports.SecurityOutputAdded{Instance: 1, Output: "OUT-1"})
		s.applySecurityEvent(ports.SecurityOutputAdded{Instance: 2, Output: "OUT-2"})
		s.applySecurityEvent(ports.SecurityBackendBarrier{Generation: state.Generation})
		s.applySecurityEvent(ports.SecurityOutputProof{Proof: ports.OutputProtection{Generation: state.Generation, Instance: 1, Kind: ports.ProtectionProtectedFrame}})
	}) {
		t.Fatal("display stopped")
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if locked != 0 {
		t.Fatal("locked without second output proof")
	}
	if !s.display.Do(func() {
		s.applySecurityEvent(ports.SecurityOutputProof{Proof: ports.OutputProtection{Generation: state.Generation, Instance: 2, Kind: ports.ProtectionInactiveOutput}})
	}) {
		t.Fatal("display stopped")
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if locked != 1 {
		t.Fatalf("locked events %d, want 1", locked)
	}
	// Closing the owner connection never opens the session.
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if !s.display.Do(func() {}) {
		t.Fatal("display stopped")
	}
	if !gate.Snapshot().Protected {
		t.Fatal("owner death unlocked")
	}
}
