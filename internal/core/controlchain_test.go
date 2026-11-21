package core_test

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// A control "do" runs a ; chain of actions, and rejects the whole chain,
// running nothing, when one of its actions is unknown.
func TestControlDoChain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		control := make(chan ports.ControlRequest)
		output := make(chan ports.OutputEvent, 1)
		client := make(chan ports.ClientEvent, 8)
		commands := make(chan ports.ClientCommand, 256)
		cfg := scrollDefaults()
		cfg.Layout.MaxColumns = 5
		c, err := core.New(cfg, core.Channels{Control: control, Output: output, Client: client, Commands: commands, Scenes: make(chan []domain.Scene, 1)}, testOptions(t, core.Options{}))
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
		output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "OUT-1", Width: 500, Height: 100}}
		for id := domain.WindowID(1); id <= 5; id++ {
			client <- ports.WindowMapped{ID: id}
		}
		synctest.Wait()
		focused(commands)
		reply := make(chan ports.ControlReply, 1)
		do := func(action string) ports.ControlReply {
			control <- ports.ControlDo{Action: action, Caps: domain.CapAct, Wait: domain.WaitCommitted, Reply: reply}
			return receive(t, reply)
		}
		if got := do("focus-column-left; focus-column-left"); got.Err != "" {
			t.Fatalf("chain: %+v", got)
		}
		if got := focused(commands); len(got) != 2 || got[0] != 4 || got[1] != 3 {
			t.Fatalf("chain focus %v, want [4 3]", got)
		}
		for _, action := range []string{"focus-column-left; bogus", "", " ; "} {
			if got := do(action); got.Err != ports.ControlErrUnknownCommand {
				t.Fatalf("%q: %+v, want unknown-command", action, got)
			}
		}
		if got := focused(commands); len(got) != 0 {
			t.Fatalf("rejected chain changed focus: %v", got)
		}
		if got := do("none"); got.Err != "" {
			t.Fatalf("none: %+v", got)
		}
	})
}
