package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

func stashSpawnCore(t *testing.T, commands chan ports.ClientCommand, spawn chan ports.SpawnRequest) *Core {
	t.Helper()
	var cfg ports.Config
	cfg.Keyboard.CmdKey = "alt"
	cfg.Layout.MaxColumns = 2
	c, err := New(cfg, Channels{Scenes: make(chan []ports.Scene, 1), Commands: commands, Spawn: spawn}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c.addScreen(ports.OutputInfo{Name: "A", Width: 300, Height: 200})
	return c
}

func claimToken(req ports.SpawnRequest) string {
	return strings.TrimPrefix(req.Env[0], ports.SlotEnv+"=")
}

// Wayland reads tokens only while slots are pending: the flag is sent before
// the spawn can reach the launcher.
func TestSpawnStashPublishesPendingBeforeSpawn(t *testing.T) {
	commands := make(chan ports.ClientCommand) // unbuffered: sends are observable
	spawn := make(chan ports.SpawnRequest, 1)
	c := stashSpawnCore(t, commands, spawn)
	done := make(chan error, 1)
	go func() {
		done <- c.spawnStash(context.Background(), c.cur().mon.Current(), [][]string{{"firefox"}}, false)
	}()
	// The send blocks on the unread command: nothing may be spawned yet.
	select {
	case req := <-spawn:
		t.Fatalf("spawned before the pending flag was delivered: %v", req)
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case v := <-commands:
		if p, ok := v.(ports.SlotsPending); !ok || !p.Pending {
			t.Fatalf("command %v", v)
		}
	case <-time.After(time.Second):
		t.Fatal("no SlotsPending")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(spawn) != 1 {
		t.Fatal("no spawn")
	}
}

// A send the launcher queue suppresses leaves no claim behind, and the
// pending flag falls again at the next publish.
func TestSpawnStashSuppressedLeavesNoClaim(t *testing.T) {
	commands := make(chan ports.ClientCommand, 8)
	c := stashSpawnCore(t, commands, nil) // no launcher queue: every send is suppressed
	if err := c.spawnStash(context.Background(), c.cur().mon.Current(), [][]string{{"a"}, {"b"}}, false); err != nil {
		t.Fatal(err)
	}
	if c.placement.anyPending() {
		t.Fatal("suppressed claims stayed pending")
	}
	if v := (<-commands).(ports.SlotsPending); !v.Pending {
		t.Fatal("pending not announced")
	}
	if err := c.publishPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := (<-commands).(ports.SlotsPending); v.Pending {
		t.Fatal("stale pending flag not republished")
	}
}

// An unanswered claim expires like a terminal's, even if no publish ran
// between its age and the window mapping.
func TestStashClaimExpires(t *testing.T) {
	c := stashSpawnCore(t, nil, nil)
	m := c.cur().mon
	m.AddWindow(1)
	target := m.Current()
	m.show(m.Workspaces[1])
	req := c.placement.request([]string{"firefox"}, spawnTarget{kind: spawnStash, ws: target, at: time.Now().Add(-2 * termRetry)})
	c.placement.place(c, ports.WindowMapped{ID: 5, Slot: claimToken(req)})
	if _, w := c.screenOf(5); w != m.Current() || len(target.Stash) != 0 {
		t.Fatal("expired claim still placed the window in the stash")
	}
}

// A workspace that left every screen no longer takes the window.
func TestStashClaimWithoutWorkspaceMapsNormally(t *testing.T) {
	c := stashSpawnCore(t, nil, nil)
	req := c.placement.request([]string{"firefox"}, spawnTarget{kind: spawnStash, ws: &Workspace{}, at: time.Now()})
	c.placement.place(c, ports.WindowMapped{ID: 5, Slot: claimToken(req)})
	if _, w := c.screenOf(5); w != c.cur().mon.Current() {
		t.Fatal("window did not map onto the current workspace")
	}
	if c.placement.anyPending() {
		t.Fatal("claim kept")
	}
}
