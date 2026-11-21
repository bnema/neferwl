package core_test

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/core"
	"github.com/bnema/neferwl/internal/domain"
)

func TestBindChainRunsEachAction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "focus-column-left; focus-column-left")
		r.send(cmdX)
		synctest.Wait()
		if got := r.focused(); len(got) != 2 || got[0] != 4 || got[1] != 3 {
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
		r.send(cmdX)
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
		r.send(cmdX)
		synctest.Wait()
		select {
		case err := <-r.done:
			if !errors.Is(err, core.ErrQuit) {
				t.Fatalf("run: %v, want ErrQuit", err)
			}
		default:
			t.Fatal("core still running after quit")
		}
		if got := r.focused(); len(got) != 0 {
			t.Fatalf("focus after quit: %v", got)
		}
	})
}

func TestBindChainDoesNotRepeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "set-column-width +1; focus-column-left")
		r.send(cmdX)
		synctest.Wait()
		if got := r.focused(); len(got) != 1 {
			t.Fatalf("press: focus %v, want one change", got)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if got := r.focused(); len(got) != 0 {
			t.Fatalf("held: focus %v, want no repeat", got)
		}
	})
}
