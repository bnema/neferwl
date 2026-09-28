package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// A monitor can lose a named workspace while its slot is still configured.
// The claim must remain pending, but its window follows ordinary placement.
func TestSlotClaimWithoutWorkspace(t *testing.T) {
	// A config message would remove both the workspace and its slot.
	// Exercise the registry's missing-destination branch directly instead.
	cfg := ports.Config{}
	cfg.Keyboard.CmdKey = "alt"
	cfg.Layout.MaxColumns = 1
	c, err := New(cfg, Channels{Scenes: make(chan []ports.Scene, 1)})
	if err != nil {
		t.Fatal(err)
	}
	key := slotKey{workspace: "gone", index: 1}
	c.slots[key] = &slotState{argv: []string{"foot"}}
	req := c.spawnSlot(key)
	token := strings.TrimPrefix(req.Env[0], ports.SlotEnv+"=")
	c.placement.place(c, ports.WindowMapped{ID: 21, Slot: token})
	if !c.placement.slotPending(key) {
		t.Fatal("unplaceable slot consumed its token")
	}
	if s, _ := c.screenOf(21); s == nil {
		t.Fatal("window did not map normally")
	}
	// Claim status is also reflected on the client command port.
	commands := make(chan ports.ClientCommand, 1)
	c.ch.Commands = commands
	if err := c.publishPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := (<-commands).(ports.SlotsPending); !got.Pending {
		t.Fatal("pending claim was not retained")
	}
}

func TestExpiredTerminalTokenMapsNormally(t *testing.T) {
	cfg := ports.Config{}
	cfg.Keyboard.CmdKey = "alt"
	cfg.Layout.MaxColumns = 1
	c, err := New(cfg, Channels{Scenes: make(chan []ports.Scene, 1)})
	if err != nil {
		t.Fatal(err)
	}
	// Keep a different, focused workspace for the ordinary-window fallback.
	c.cur().mon.AddWindow(1)
	target := c.cur().mon.Current()
	c.cur().mon.show(c.cur().mon.Workspaces[1])
	focused := c.cur().mon.Current()
	if focused == target {
		t.Fatal("focus did not move to another workspace")
	}
	t0 := time.Unix(123, 0)
	req := c.placement.request([]string{"foot"}, spawnTarget{terminal: target, at: t0})
	token := strings.TrimPrefix(req.Env[0], ports.SlotEnv+"=")
	c.placement.expireTerminals(t0.Add(termRetry))
	if _, ok := c.placement.pending[token]; !ok {
		t.Fatal("claim expired at retry boundary")
	}
	c.placement.expireTerminals(t0.Add(termRetry + time.Nanosecond))
	if _, ok := c.placement.pending[token]; ok {
		t.Fatal("claim survived past retry boundary")
	}
	c.placement.place(c, ports.WindowMapped{ID: 51, Slot: token})
	if _, w := c.screenOf(51); w != focused || target.has(51) {
		t.Fatal("expired token did not map onto focused workspace")
	}
}
