package launcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/adapters/sessionsecurity"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

func TestSpawnAdmissionRequiresCurrentUnlockedEpoch(t *testing.T) {
	var gate sessionsecurity.Gate
	l := New(nil, logging.For(context.Background(), "launcher"))
	l.Security = &gate
	if !l.admitted(ports.SpawnRequest{}) {
		t.Fatal("initial unlocked request refused")
	}
	protected, err := gate.Engage()
	if err != nil {
		t.Fatal(err)
	}
	if l.admitted(ports.SpawnRequest{}) || l.admitted(ports.SpawnRequest{Security: protected}) {
		t.Fatal("protected spawn admitted")
	}
	unlocked, err := gate.Release(protected.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if l.admitted(ports.SpawnRequest{}) || l.admitted(ports.SpawnRequest{Security: protected}) {
		t.Fatal("old spawn admitted after release")
	}
	if !l.admitted(ports.SpawnRequest{Security: unlocked}) {
		t.Fatal("current spawn refused")
	}
}

func TestSpawnRechecksBeforeProcessStart(t *testing.T) {
	ctx := context.Background()
	security := portsmocks.NewMockSessionSecurity(t)
	security.EXPECT().Snapshot().Return(ports.SecurityState{}).Once()
	security.EXPECT().Snapshot().Return(ports.SecurityState{Generation: 1, Protected: true}).Once()
	l := New([]string{"PATH=/bin"}, logging.For(ctx, "launcher"))
	l.Security = security
	result := filepath.Join(t.TempDir(), "must-not-exist")
	requests := make(chan ports.SpawnRequest, 1)
	requests <- ports.SpawnRequest{Argv: []string{"touch", result}}
	close(requests)
	if err := l.Run(ctx, requests); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(result); !os.IsNotExist(err) {
		t.Fatalf("stale native spawn ran: %v", err)
	}
}
