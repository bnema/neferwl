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

func TestBindTraceNotable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		chain   string
		action  string
		notable bool
	}{
		{"move", "move-column-left", "move-column-left", true},
		{"resize", "set-column-width +1", "set-column-width +1", true},
		{"move to workspace n", "move-column-to-workspace 2", "move-column-to-workspace 2", true},
		{"tabbed", "toggle-column-tabbed", "toggle-column-tabbed", true},
		{"focus", "focus-column-left", "focus-column-left", false},
		{"focus workspace n", "focus-workspace 2", "focus-workspace 2", false},
		{"spawn", "spawn foot", "spawn foot", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := runChainRig(t, tc.chain)
				r.send(cmdX)
				synctest.Wait()
				b := lastBindTrace(t, r)
				if b.Action != tc.action || b.Notable != tc.notable || b.Window != 5 {
					t.Fatalf("trace %+v, want action %q notable %v window 5", b, tc.action, tc.notable)
				}
			})
		})
	}
}

func TestBindTraceEdgeFocus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRigWith(t, "focus-window-down", func(cfg *domain.Config) { cfg.Navigation.Edge = domain.NavEdgeWorkspace })
		r.mustDo(t, "move-column-to-workspace 2; focus-workspace 1")
		r.drain()
		r.send(cmdX)
		synctest.Wait()
		var edge *domain.BindDispatched
		for len(r.binds) > 0 {
			if b, ok := (<-r.binds).(domain.BindDispatched); ok && b.Action == "edge-focus" {
				edge = &b
			}
		}
		if edge == nil || edge.Notable || edge.Source != domain.BindSourceEdge || edge.WorkspaceAfter != 2 {
			t.Fatalf("edge trace %+v, want a debug edge-focus to workspace 2", edge)
		}
	})
}

func TestBindTraceTargetWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "none")
		r.mustDo(t, "focus-column-left; mark a; focus-column-right")
		r.drain()
		r.mustTarget(t, "a", "toggle-floating")
		if b := lastBindTrace(t, r); b.Action != "toggle-floating" || !b.Notable || b.Window != 4 {
			t.Fatalf("trace %+v, want notable toggle-floating on window 4", b)
		}
	})
}

func TestBindTraceRepeatNotNotable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := runChainRig(t, "move-column-left")
		r.send(cmdX)
		synctest.Wait()
		if b := lastBindTrace(t, r); !b.Notable || b.Source != domain.BindSourceKey {
			t.Fatalf("press trace %+v, want notable key", b)
		}
		r.drain()
		time.Sleep(time.Second)
		synctest.Wait()
		b := lastBindTrace(t, r)
		if b.Source != domain.BindSourceRepeat || b.Notable {
			t.Fatalf("repeat trace %+v, want a debug repeat", b)
		}
	})
}

// lastBindTrace returns the last bind trace sent since the last drain.
func lastBindTrace(t *testing.T, r chainRig) domain.BindDispatched {
	t.Helper()
	var last *domain.BindDispatched
	for len(r.binds) > 0 {
		if b, ok := (<-r.binds).(domain.BindDispatched); ok {
			last = &b
		}
	}
	if last == nil {
		t.Fatal("no bind trace")
		return domain.BindDispatched{}
	}
	return *last
}
