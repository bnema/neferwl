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

func TestControlInjectInputPath(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var epoch atomic.Value
		epoch.Store(domain.SecurityState{})
		// flip, when set, becomes the epoch after skip more snapshots: the
		// security state changes in the middle of an injected event.
		var flip atomic.Pointer[domain.SecurityState]
		var skip atomic.Int32
		gate := portsmocks.NewMockSessionSecurity(t)
		gate.EXPECT().Snapshot().RunAndReturn(func() domain.SecurityState {
			cur := epoch.Load().(domain.SecurityState)
			if flip.Load() != nil && skip.Add(-1) < 0 {
				epoch.Store(*flip.Swap(nil))
			}
			return cur
		})
		var cfg domain.Config
		cfg.Keyboard.CmdKey = "super"
		cfg.Layout.MaxColumns = 2
		cfg.Binds = map[string]string{"Super+s": "focus-workspace-next", "Super+a": "focus-workspace-prev"}
		control := make(chan ports.ControlRequest)
		input := make(chan ports.InputEvent)
		output := make(chan ports.OutputEvent)
		client := make(chan ports.ClientEvent)
		c, err := New(cfg, Channels{Control: control, Input: input, Output: output, Client: client, Commands: make(chan ports.ClientCommand, 128), Scenes: make(chan []domain.Scene, 1)}, testOptions(t, Options{Security: gate}))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- c.Run(ctx) }()
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}()
		output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "A", Width: 100, Height: 80}}
		client <- ports.WindowMapped{ID: 1}
		synctest.Wait()
		reply := make(chan ports.ControlReply, 1)
		stamp := func(ev ports.InputEvent) ports.InputEvent {
			return ports.SecurityInput{State: epoch.Load().(domain.SecurityState), Event: ev}
		}
		key := func(name string, pressed bool) ports.InputEvent {
			return stamp(ports.KeyEvent{Keysym: name, Mods: domain.ModSuper, Pressed: pressed})
		}
		read := func() ports.ControlReply {
			t.Helper()
			synctest.Wait()
			select {
			case r := <-reply:
				return r
			default:
				t.Fatal("no reply")
				return ports.ControlReply{}
			}
		}
		control <- ports.ControlInject{Caps: domain.CapInput, Wait: domain.WaitSettled, Events: []ports.InputEvent{stamp(ports.InputDeviceAdded{ID: 42, Name: "control-device"}), key("s", true), key("s", false)}, Reply: reply}
		r := read()
		if r.Err != "" || r.Done != domain.WaitSettled || len(r.Diff.Workspaces) != 1 || r.State.Outputs[0].Active != 2 || c.inputNames[42] != "control-device" {
			t.Fatalf("inject: %+v devices %v", r, c.inputNames)
		}
		// Native input uses the same helper and can switch back.
		input <- key("a", true)
		input <- key("a", false)
		synctest.Wait()
		if c.state().Outputs[0].Active != 1 {
			t.Fatal("native bind path differs")
		}
		control <- ports.ControlInject{Events: []ports.InputEvent{key("s", true)}, Reply: reply}
		if r := read(); r.Err != ports.ControlErrForbidden {
			t.Fatal(r)
		}
		if c.state().Outputs[0].Active != 1 {
			t.Fatal("forbidden input ran")
		}
		stale := key("s", true)
		epoch.Store(domain.SecurityState{Generation: 1, Protected: true})
		control <- ports.ControlInject{Caps: domain.CapInput, Events: []ports.InputEvent{stale, stamp(ports.InputDeviceAdded{ID: 99, Name: "blocked"})}, Reply: reply}
		if r := read(); r.Err != ports.ControlErrLocked {
			t.Fatal(r)
		}
		if c.state().Outputs[0].Active != 1 || c.inputNames[99] != "" {
			t.Fatal("locked batch ran")
		}
		epoch.Store(domain.SecurityState{Generation: 2})
		control <- ports.ControlInject{Caps: domain.CapInput, Events: []ports.InputEvent{stale}, Reply: reply}
		if r := read(); r.Err != "" || r.State.Outputs[0].Active != 1 {
			t.Fatalf("stale epoch input admitted: %+v", r)
		}
		// The epoch changes mid-batch: the batch is answered, not dropped.
		// Two snapshots in: after the first checkpoints, inside the bind.
		skip.Store(2)
		flip.Store(&domain.SecurityState{Generation: 3, Protected: true})
		control <- ports.ControlInject{Caps: domain.CapInput, Events: []ports.InputEvent{key("s", true), key("s", false)}, Reply: reply}
		if r := read(); r.Err != ports.ControlErrLocked {
			t.Fatalf("interrupted batch: %+v", r)
		}
		// Locked: actions are refused, quit still runs.
		control <- ports.ControlDo{Caps: domain.CapAct, Action: "focus-workspace-next", Reply: reply}
		if r := read(); r.Err != ports.ControlErrLocked {
			t.Fatalf("locked action: %+v", r)
		}
		control <- ports.ControlDo{Caps: domain.CapAct, Action: "quit", Reply: reply}
		if r := read(); r.Err != "" {
			t.Fatalf("locked quit: %+v", r)
		}
		if err := <-done; !errors.Is(err, ErrQuit) {
			t.Fatalf("quit: %v", err)
		}
		done <- nil
	})
}
