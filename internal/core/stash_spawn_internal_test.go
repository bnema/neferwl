package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

// claimBase is the instant of the fixed test clock.
var claimBase = time.Unix(1000, 0)

// fixedClock is a mock clock at *now, which a test moves by assigning.
func fixedClock(t *testing.T, now *time.Time) ports.Clock {
	t.Helper()
	clock := portsmocks.NewMockClock(t)
	clock.EXPECT().Now().RunAndReturn(func() time.Time { return *now }).Maybe()
	return clock
}

func stashSpawnCore(t *testing.T, commands chan ports.ClientCommand, spawn chan ports.SpawnRequest, clock ports.Clock) *Core {
	t.Helper()
	var cfg ports.Config
	cfg.Keyboard.CmdKey = "alt"
	cfg.Layout.MaxColumns = 2
	c, err := New(cfg, Channels{Scenes: make(chan []domain.Scene, 1), Commands: commands, Spawn: spawn}, Options{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	c.addScreen(ports.OutputInfo{Name: "A", Width: 300, Height: 200})
	return c
}

func claimToken(req ports.SpawnRequest) string {
	return strings.TrimPrefix(req.Env[0], ports.SlotEnv+"=")
}

// A stalled command transport must not delay a placement spawn.
func TestSpawnStashIndependentOfCommandDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := claimBase
		commands := make(chan ports.ClientCommand)
		spawn := make(chan ports.SpawnRequest, 1)
		c := stashSpawnCore(t, commands, spawn, fixedClock(t, &now))
		if err := c.spawnStash(context.Background(), c.cur().mon.Current(), [][]string{{"firefox"}}, false); err != nil {
			t.Fatal(err)
		}
		req := <-spawn
		c.placement.place(c, ports.WindowMapped{ID: 5, Slot: claimToken(req)})
		if w := c.cur().mon.Current(); len(w.Stash) != 1 || w.Stash[0].ID != 5 {
			t.Fatal("placement lost while commands unread")
		}

	})
}

// A send the launcher queue suppresses leaves no claim behind.
func TestSpawnStashSuppressedLeavesNoClaim(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := claimBase
		commands := make(chan ports.ClientCommand, 8)
		c := stashSpawnCore(t, commands, nil, fixedClock(t, &now)) // no launcher queue: every send is suppressed
		if err := c.spawnStash(context.Background(), c.cur().mon.Current(), [][]string{{"a"}, {"b"}}, false); err != nil {
			t.Fatal(err)
		}
		if c.placement.anyPending() {
			t.Fatal("suppressed claims stayed pending")
		}

	})
}

// From the first command that is not issued, the rest are not sent and their
// claims are cancelled; the issued ones keep theirs.
func TestSpawnStashCancelsClaimsFromFirstUnissued(t *testing.T) {
	for _, tc := range []struct {
		name      string
		queue     int
		cancelled bool
		issued    int
		wantErr   error
	}{
		{"queue of one", 1, false, 1, nil},
		{"context cancelled", 4, true, 0, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				now := claimBase
				commands := make(chan ports.ClientCommand) // unread: spawns are independent of command delivery
				spawn := make(chan ports.SpawnRequest, tc.queue)
				c := stashSpawnCore(t, commands, spawn, fixedClock(t, &now))
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				if tc.cancelled {
					cancel()
				}
				err := c.spawnStash(ctx, c.cur().mon.Current(), [][]string{{"a"}, {"b"}}, false)
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error %v, want %v", err, tc.wantErr)
				}
				if len(spawn) != tc.issued {
					t.Fatalf("%d spawns, want %d", len(spawn), tc.issued)
				}
				if len(c.placement.pending) != tc.issued {
					t.Fatalf("%d claims kept, want %d", len(c.placement.pending), tc.issued)
				}
				if tc.issued == 1 {
					if _, ok := c.placement.pending[claimToken(<-spawn)]; !ok {
						t.Fatal("the issued command lost its claim")
					}
				}

			})
		})
	}
}

// A claim is placed up to termRetry after it was made, and not a moment
// later, even if no publish ran between its age and the window mapping.
func TestStashClaimExpiry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		age    time.Duration
		placed bool
	}{
		{"fresh", 0, true},
		{"at the limit", termRetry, true},
		{"just after", termRetry + time.Nanosecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				now := claimBase
				c := stashSpawnCore(t, nil, nil, fixedClock(t, &now))
				m := c.cur().mon
				m.AddWindow(1)
				target := m.Current()
				m.show(m.Workspaces[1])
				req := c.placement.request([]string{"firefox"}, spawnTarget{kind: spawnStash, ws: target, at: now})
				now = claimBase.Add(tc.age)
				c.placement.place(c, ports.WindowMapped{ID: 5, Slot: claimToken(req)})
				_, w := c.screenOf(5)
				if placed := w == target && len(target.Stash) == 1; placed != tc.placed {
					t.Fatalf("placed %t, want %t", placed, tc.placed)
				}

			})
		})
	}
}

// A workspace that left every screen no longer takes the window.
func TestStashClaimWithoutWorkspaceMapsNormally(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := claimBase
		c := stashSpawnCore(t, nil, nil, fixedClock(t, &now))
		req := c.placement.request([]string{"firefox"}, spawnTarget{kind: spawnStash, ws: &Workspace{}, at: now})
		c.placement.place(c, ports.WindowMapped{ID: 5, Slot: claimToken(req)})
		if _, w := c.screenOf(5); w != c.cur().mon.Current() {
			t.Fatal("window did not map onto the current workspace")
		}
		if c.placement.anyPending() {
			t.Fatal("claim kept")
		}

	})
}
