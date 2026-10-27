package core

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

func TestPlacementUsesTokenSource(t *testing.T) {
	tokens := portsmocks.NewMockTokenSource(t)
	tokens.EXPECT().Token().Return("placement-token").Once()
	placement := newSpawnPlacement(tokens)
	target := spawnTarget{kind: spawnStash, ws: &Workspace{}}
	req := placement.request([]string{"terminal"}, target)
	if len(req.Env) != 1 || req.Env[0] != ports.SlotEnv+"=placement-token" {
		t.Fatalf("spawn environment: %v", req.Env)
	}
	if placement.pending["placement-token"] != target {
		t.Fatal("token did not register target")
	}
}

func TestConfigEqualityDoesNotAllocate(t *testing.T) {
	cfg := domain.Config{Workspaces: []domain.WorkspaceConfig{{Slots: []domain.SlotConfig{{Argv: []string{"terminal"}}}}}}
	if n := testing.AllocsPerRun(100, func() {
		if !cfg.Equal(cfg) {
			t.Fatal("config differs")
		}
	}); n != 0 {
		t.Fatalf("config equality allocates %v", n)
	}
}
