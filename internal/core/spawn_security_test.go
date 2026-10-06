package core

import (
	"context"
	"sync/atomic"
	"testing"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

func TestFullSpawnQueueSlotsTerminalsExplicitRemainTransitionSafe(t *testing.T) {
	for _, kind := range []string{"slot", "terminal", "explicit", "startup"} {
		t.Run(kind, func(t *testing.T) {
			c, _, commands := hiddenCaptureCommands(t)
			c.configures.cw.reset()
			c.cur().mon.RemoveWindow(1)
			c.cur().mon.RemoveWindow(2)
			var epoch atomic.Value
			epoch.Store(ports.SecurityState{Generation: 2})
			gate := portsmocks.NewMockSessionSecurity(t)
			gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState { return epoch.Load().(ports.SecurityState) })
			c.opts.Security = gate
			c.syncSecurity()
			spawn := make(chan ports.SpawnRequest, 1)
			c.ch.Spawn = spawn
			sentinel := ports.SpawnRequest{Argv: []string{"occupied"}}
			spawn <- sentinel
			key := slotKey{workspace: "dev", index: 1}
			c.cfg.Terminal.Command = []string{"terminal"}
			c.cfg.Terminal.AutoOpen = "always"
			c.opts.Terminal = kind == "terminal"
			if kind == "slot" {
				c.slots[key] = &slotState{argv: []string{"slot"}}
				c.toSpawn = []slotKey{key}
			}
			if kind == "startup" {
				c.startup = [][]string{{"startup"}}
			}
			c.binds = map[binding]Action{{key: "t"}: ActionSpawnTerminal}
			attempt := func() error {
				switch kind {
				case "slot":
					return c.spawnSlots(context.Background(), false)
				case "terminal":
					return c.spawnEmpty(context.Background())
				case "explicit":
					return c.handleInput(context.Background(), ports.KeyEvent{Keysym: "t", Pressed: true})
				default:
					c.spawnStartup(context.Background())
					return nil
				}
			}
			done := make(chan error, 1)
			go func() { done <- attempt() }()
			if err := reviewReceive(t, done); err != nil {
				t.Fatal(err)
			}
			if len(spawn) != 1 {
				t.Fatal("full queue changed")
			}
			if c.placement.anyPending() {
				t.Fatal("unsent spawn kept claim")
			}
			if kind == "slot" && len(c.toSpawn) != 1 {
				t.Fatal("deferred slot lost")
			}
			if kind == "startup" && len(c.startup) != 1 {
				t.Fatal("deferred startup lost")
			}
			// A full queue never prevents the owner from adopting protection. Empty
			// it only after transition; no path may enqueue in the protected epoch.
			epoch.Store(ports.SecurityState{Generation: 3, Protected: true})
			c.syncSecurity()
			<-spawn
			if err := attempt(); err != nil {
				t.Fatal(err)
			}
			if len(spawn) != 0 {
				t.Fatal("protected spawn enqueued")
			}
			epoch.Store(ports.SecurityState{Generation: 4})
			c.syncSecurity()
			if err := attempt(); err != nil {
				t.Fatal(err)
			}
			req := reviewReceive(t, spawn)
			if req.Security != (ports.SecurityState{Generation: 4}) {
				t.Fatalf("missing owner stamp: %+v", req)
			}
			// Queued requests retain admission generation, not a later gate value.
			epoch.Store(ports.SecurityState{Generation: 5, Protected: true})
			if req.Security.Generation != 4 {
				t.Fatal("queued request relabeled")
			}
			for len(commands) > 0 {
				<-commands
			}
		})
	}
}

func TestRunStartupDrainsWithoutFurtherOwnerEvents(t *testing.T) {
	c, _, _ := hiddenCaptureCommands(t)
	c.cfg.Startup = [][]string{{"one"}, {"two"}, {"three"}}
	spawn := make(chan ports.SpawnRequest, 1)
	spawn <- ports.SpawnRequest{Argv: []string{"occupied"}}
	c.ch.Spawn = spawn
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	if got := reviewReceive(t, spawn); got.Argv[0] != "occupied" {
		t.Fatal(got)
	}
	for _, want := range []string{"one", "two", "three"} {
		if got := reviewReceive(t, spawn); got.Argv[0] != want {
			t.Fatal(got)
		}
	}
	cancel()
	if err := reviewReceive(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestRunStartupPendingDisabledWhileLockedResumesOnUnlockEvent(t *testing.T) {
	c, _, _ := hiddenCaptureCommands(t)
	c.cfg.Startup = [][]string{{"one"}, {"two"}, {"three"}}
	var epoch atomic.Value
	locked := ports.SecurityState{Generation: 1, Protected: true}
	epoch.Store(locked)
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState { return epoch.Load().(ports.SecurityState) })
	c.opts.Security = gate
	spawn := make(chan ports.SpawnRequest, 1)
	c.ch.Spawn = spawn
	client := make(chan ports.ClientEvent, 1)
	c.ch.Client = client
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	client <- ports.SessionLockChanged{State: locked}
	reviewReceive(t, c.ch.Scenes) // locked event has completed, no startup sent
	select {
	case req := <-spawn:
		t.Fatalf("locked startup sent: %+v", req)
	default:
	}
	unlocked := ports.SecurityState{Generation: 2}
	epoch.Store(unlocked)
	client <- ports.SessionLockChanged{State: unlocked}
	for _, want := range []string{"one", "two", "three"} {
		got := reviewReceive(t, spawn)
		if got.Argv[0] != want || got.Security != unlocked {
			t.Fatal(got)
		}
	}
	cancel()
	if err := reviewReceive(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestStartupSuccessfulHandoffConsumesDespiteImmediateTransition(t *testing.T) {
	c, _, _ := hiddenCaptureCommands(t)
	c.startup = [][]string{{"once"}}
	spawn := make(chan ports.SpawnRequest, 1)
	c.ch.Spawn = spawn
	var epoch atomic.Value
	epoch.Store(ports.SecurityState{})
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState {
		if len(spawn) != 0 {
			epoch.Store(ports.SecurityState{Generation: 1, Protected: true})
		}
		return epoch.Load().(ports.SecurityState)
	})
	c.opts.Security = gate
	c.spawnStartup(context.Background())
	if len(c.startup) != 0 {
		t.Fatal("successful handoff kept pending startup")
	}
	if !gate.Snapshot().Protected {
		t.Fatal("test did not engage gate after handoff")
	}
	epoch.Store(ports.SecurityState{Generation: 2})
	req := <-spawn
	if req.Argv[0] != "once" || req.Security != (ports.SecurityState{}) {
		t.Fatal(req)
	}
	c.syncSecurity()
	c.spawnStartup(context.Background())
	if len(spawn) != 0 {
		t.Fatal("successful startup duplicated after release")
	}
}

func TestRunSelectableStartupConsumedOnceAcrossReceiverTransition(t *testing.T) {
	c, _, _ := hiddenCaptureCommands(t)
	c.cfg.Startup = [][]string{{"once"}}
	var epoch atomic.Value
	epoch.Store(ports.SecurityState{})
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState { return epoch.Load().(ports.SecurityState) })
	c.opts.Security = gate
	spawn := make(chan ports.SpawnRequest)
	c.ch.Spawn = spawn
	client := make(chan ports.ClientEvent, 1)
	c.ch.Client = client
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	req := reviewReceive(t, spawn)
	if req.Argv[0] != "once" {
		t.Fatal(req)
	}
	locked := ports.SecurityState{Generation: 1, Protected: true}
	epoch.Store(locked)
	client <- ports.SessionLockChanged{State: locked}
	reviewReceive(t, c.ch.Scenes)
	unlocked := ports.SecurityState{Generation: 2}
	epoch.Store(unlocked)
	client <- ports.SessionLockChanged{State: unlocked}
	for {
		if s := reviewReceive(t, c.ch.Scenes); s[0].Security == unlocked {
			break
		}
	}
	select {
	case duplicate := <-spawn:
		t.Fatalf("startup replayed: %+v", duplicate)
	default:
	}
	cancel()
	if err := reviewReceive(t, done); err != nil {
		t.Fatal(err)
	}
	if len(c.startup) != 0 {
		t.Fatal("startup not consumed")
	}
}
