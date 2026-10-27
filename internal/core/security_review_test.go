package core

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

func reviewReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	synctest.Wait()
	select {
	case v := <-ch:
		return v
	default:
		t.Fatal("timed out")
		var z T
		return z
	}
}

func TestInputOutputSwitchBackpressureKeepsAdmittedEpoch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _, _ := hiddenCaptureCommands(t)
		c.addScreen(ports.OutputInfo{Name: "B", Width: 200, Height: 100})
		var state atomic.Value
		state.Store(domain.SecurityState{})
		gate := portsmocks.NewMockSessionSecurity(t)
		var calls atomic.Int32
		gate.EXPECT().Snapshot().RunAndReturn(func() domain.SecurityState {
			v := state.Load().(domain.SecurityState)
			if calls.Add(1) == 2 {
				// The epoch changes right after the check that admits the command.
				state.Store(domain.SecurityState{Generation: 2})
			}
			return v
		})
		c.opts.Security = gate
		c.sent.outputs = ports.SetOutputs{Outputs: c.layout(), Focused: c.cur().name(), Off: c.offOutputs()}
		commands := make(chan ports.ClientCommand, 8)
		c.ch.Commands = commands
		err := c.handleInput(context.Background(), ports.PointerMotion{X: 350, Y: 30})
		cmd := reviewReceive(t, commands).(ports.SecurityCommand)
		if cmd.State != (domain.SecurityState{}) {
			t.Fatalf("old handler adopted new epoch: %+v", cmd)
		}
		if !errors.Is(err, errSecurityChanged) {
			t.Fatalf("handler not aborted: %v", err)
		}
		if c.security != (domain.SecurityState{}) {
			t.Fatal("publish adopted generation mid-input")
		}
		select {
		case cmd := <-commands:
			t.Fatalf("remaining old input command: %+v", cmd)
		default:
		}

	})
}

func TestExactEpochAdmissionPreservesProducerModifierState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _, _ := hiddenCaptureCommands(t)
		state := domain.SecurityState{Generation: 2}
		gate := portsmocks.NewMockSessionSecurity(t)
		gate.EXPECT().Snapshot().Return(state)
		c.opts.Security = gate
		c.syncSecurity()
		key := ports.KeyEvent{Keycode: 58, Keysym: "Control_L", Pressed: true, Mods: domain.ModCtrl, State: ports.ModState{Depressed: 1 << 19, Latched: 1 << 21, Locked: 1 << 23, Group: 2}}
		ev, ok := c.admitInput(ports.SecurityInput{State: state, Event: key})
		if !ok || ev.(ports.KeyEvent) != key {
			t.Fatalf("producer state reinterpreted: %+v", ev)
		}
		if _, ok := c.admitInput(ports.SecurityInput{State: domain.SecurityState{Generation: 1}, Event: key}); ok {
			t.Fatal("stale producer epoch admitted")
		}

	})
}

func TestEpochChangeSuppressesSpawnAndDefersSlots(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _, _ := hiddenCaptureCommands(t)
		key := slotKey{workspace: "dev", index: 1}
		c.slots[key] = &slotState{argv: []string{"terminal"}}
		c.toSpawn = []slotKey{key}
		var state atomic.Value
		state.Store(domain.SecurityState{})
		gate := portsmocks.NewMockSessionSecurity(t)
		var calls atomic.Int32
		gate.EXPECT().Snapshot().RunAndReturn(func() domain.SecurityState {
			v := state.Load().(domain.SecurityState)
			if calls.Add(1) == 2 {
				// The gate locks right after the check that admits the slot.
				state.Store(domain.SecurityState{Generation: 1, Protected: true})
			}
			return v
		})
		c.opts.Security = gate
		commands := make(chan ports.ClientCommand, 8)
		c.ch.Commands = commands
		spawn := make(chan ports.SpawnRequest, 2)
		c.ch.Spawn = spawn
		err := c.spawnSlots(context.Background(), false)
		if err != nil {
			t.Fatal(err)
		}
		if len(spawn) != 0 || len(c.toSpawn) != 1 || c.placement.slotPending(key) {
			t.Fatal("spawn leaked or deferred slot lost")
		}
		// Retry after unlock, with a real buffered command sink.
		c.ch.Commands = make(chan ports.ClientCommand, 8)
		state.Store(domain.SecurityState{Generation: 2})
		c.syncSecurity()
		if err := c.spawnSlots(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		if len(spawn) != 1 || len(c.toSpawn) != 0 {
			t.Fatal("deferred slot did not retry")
		}

	})
}

func TestTerminalEpochChangeSuppressesSpawnAndRestoresRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _, _ := hiddenCaptureCommands(t)
		c.cur().mon.RemoveWindow(1)
		c.cur().mon.RemoveWindow(2)
		c.opts.Terminal = true
		c.cfg.Terminal.AutoOpen = "always"
		c.cfg.Terminal.Command = []string{"terminal"}
		var state atomic.Value
		state.Store(domain.SecurityState{})
		gate := portsmocks.NewMockSessionSecurity(t)
		var calls atomic.Int32
		gate.EXPECT().Snapshot().RunAndReturn(func() domain.SecurityState {
			v := state.Load().(domain.SecurityState)
			if calls.Add(1) == 1 {
				// The gate locks right after the check that admits the spawn.
				state.Store(domain.SecurityState{Generation: 1, Protected: true})
			}
			return v
		})
		c.opts.Security = gate
		commands := make(chan ports.ClientCommand, 8)
		c.ch.Commands = commands
		spawn := make(chan ports.SpawnRequest, 1)
		c.ch.Spawn = spawn
		err := c.spawnEmpty(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(spawn) != 0 || c.placement.anyPending() {
			t.Fatal("terminal spawn/claim crossed lock")
		}

	})
}

// The pre-existing slot test can observe a startup placeholder snapshot: a
// scene send happens before the real-output workspace publication. Reproduce
// the empty snapshot contract without intentionally panicking a test runner.
func TestStartupWorkspaceSnapshotHasNoOutputBeforeOutputAdded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cfg ports.Config
		cfg.Keyboard.CmdKey = "super"
		cfg.Layout.MaxColumns = 2
		workspaces := make(chan ports.Workspaces, 1)
		c, err := New(cfg, Channels{Scenes: make(chan []domain.Scene, 1), Workspaces: workspaces}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		c.publishWorkspaces()
		initial := <-workspaces
		if len(initial.Outputs) != 0 {
			t.Fatal(initial)
		}
		c.addScreen(ports.OutputInfo{Name: "A", Width: 100, Height: 80})
		c.publishWorkspaces()
		if next := <-workspaces; len(next.Outputs) != 1 {
			t.Fatal(next)
		}

	})
}
