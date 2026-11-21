package core_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// chainRig is a running Core with five windows in a row on one output, the
// last one focused, and cmd+x bound to chain.
type chainRig struct {
	input    chan ports.InputEvent
	commands chan ports.ClientCommand
	spawn    chan domain.SpawnRequest
	binds    chan domain.InputTrace
	done     chan error
}

func runChainRig(t *testing.T, chain string) chainRig {
	t.Helper()
	input := make(chan ports.InputEvent, 4)
	client := make(chan ports.ClientEvent, 8)
	output := make(chan ports.OutputEvent, 1)
	commands := make(chan ports.ClientCommand, 256)
	spawn := make(chan domain.SpawnRequest, 4)
	binds := make(chan domain.InputTrace, 16)
	cfg := scrollDefaults()
	cfg.Layout.MaxColumns = 5
	cfg.Binds["Cmd+x"] = chain
	c, err := core.New(cfg, core.Channels{Input: input, Client: client, Output: output, Config: make(chan domain.ConfigChanged, 1), ConfigErrors: make(chan error, 1), Commands: commands, Scenes: make(chan []domain.Scene, 1), Spawn: spawn, Binds: binds}, testOptions(t, core.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() { cancel(); synctest.Wait() })
	output <- ports.OutputAdded{Info: domain.OutputInfo{Name: "OUT-1", Width: 500, Height: 100}}
	for id := domain.WindowID(1); id <= 5; id++ {
		client <- ports.WindowMapped{ID: id}
	}
	synctest.Wait()
	for len(commands) > 0 {
		<-commands
	}
	for len(binds) > 0 {
		<-binds
	}
	return chainRig{input, commands, spawn, binds, done}
}

var cmdX = ports.KeyEvent{Keysym: "x", Keycode: 53, Mods: domain.ModSuper, Pressed: true}

// focused returns the windows focused since the last call, in order.
func focused(commands chan ports.ClientCommand) []domain.WindowID {
	var ids []domain.WindowID
	for len(commands) > 0 {
		if f, ok := (<-commands).(ports.FocusWindow); ok {
			ids = append(ids, f.ID)
		}
	}
	return ids
}

func TestBindChainRunsEachAction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "focus-column-left; focus-column-left")
		r.input <- cmdX
		synctest.Wait()
		if got := focused(r.commands); len(got) != 2 || got[0] != 4 || got[1] != 3 {
			t.Fatalf("focus %v, want [4 3]", got)
		}
		var traces []domain.BindDispatched
		for len(r.binds) > 0 {
			if b, ok := (<-r.binds).(domain.BindDispatched); ok {
				traces = append(traces, b)
			}
		}
		if len(traces) != 2 {
			t.Fatalf("traces %+v, want 2", traces)
		}
		for _, b := range traces {
			if b.Action != "focus-column-left" || b.Combo == "" || b.Combo != traces[0].Combo || b.Source != domain.BindSourceKey {
				t.Fatalf("trace %+v", b)
			}
		}
	})
}

func TestBindChainSpawnsEachCommand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "spawn a; spawn b")
		r.input <- cmdX
		synctest.Wait()
		if len(r.spawn) != 2 {
			t.Fatalf("%d spawn requests, want 2", len(r.spawn))
		}
		if a, b := <-r.spawn, <-r.spawn; a.Argv[0] != "a" || b.Argv[0] != "b" {
			t.Fatalf("spawned %q then %q", a.Argv, b.Argv)
		}
	})
}

func TestBindChainStopsAtQuit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "quit; focus-column-left")
		r.input <- cmdX
		synctest.Wait()
		select {
		case err := <-r.done:
			if !errors.Is(err, core.ErrQuit) {
				t.Fatalf("run: %v, want ErrQuit", err)
			}
		default:
			t.Fatal("core still running after quit")
		}
		if got := focused(r.commands); len(got) != 0 {
			t.Fatalf("focus after quit: %v", got)
		}
	})
}

func TestBindChainDoesNotRepeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "set-column-width +1; focus-column-left")
		r.input <- cmdX
		synctest.Wait()
		if got := focused(r.commands); len(got) != 1 {
			t.Fatalf("press: focus %v, want one change", got)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if got := focused(r.commands); len(got) != 0 {
			t.Fatalf("held: focus %v, want no repeat", got)
		}
	})
}
