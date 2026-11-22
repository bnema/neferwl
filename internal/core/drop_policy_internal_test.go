package core

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
)

// policyCore is dragCore with cfg's drop policy and named workspaces.
func policyCore(t *testing.T, edit func(*domain.Config)) *Core {
	t.Helper()
	c := dragCore(t)
	cfg := c.cfg
	edit(&cfg)
	if err := c.apply(cfg); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDropPolicyResolution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := policyCore(t, func(cfg *domain.Config) {
			cfg.Drop.SetGap(domain.DropNone)
			cfg.Drop.SetWindow(domain.DropSideTop, domain.DropSwap)
			var out, later, other domain.DropPolicy
			out.SetWindow(domain.DropSideLeft, domain.DropSwap)
			out.SetWindow(domain.DropSideTop, domain.DropNone)
			later.SetWindow(domain.DropSideTop, domain.DropStack)
			other.SetGap(domain.DropColumn)
			cfg.Drop.Outputs = []domain.OutputDrop{{Output: "OUT", DropPolicy: out}, {Output: "ELSE", DropPolicy: other}, {Output: "OUT", DropPolicy: later}}
			ws := domain.WorkspaceConfig{Name: "web"}
			ws.Drop.SetWindow(domain.DropSideLeft, domain.DropNone)
			cfg.Workspaces = []domain.WorkspaceConfig{ws}
		})
		m := c.cur().mon
		m.Name = "OUT"
		w := m.Current()
		p := c.dropPolicy(m, w)
		// Output over global over defaults; the later OUT entry wins; ELSE does not match.
		if p.Window[domain.DropSideLeft] != domain.DropSwap || p.Window[domain.DropSideTop] != domain.DropStack ||
			p.Gap != domain.DropNone || p.Window[domain.DropSideRight] != domain.DropColumn {
			t.Fatalf("numbered workspace: %+v", p)
		}
		// The workspace none overrides the output swap.
		named := *w
		named.Name = "web"
		if p := c.dropPolicy(m, &named); p.Window[domain.DropSideLeft] != domain.DropNone || p.Window[domain.DropSideTop] != domain.DropStack {
			t.Fatalf("named workspace: %+v", p)
		}
		allocs := testing.AllocsPerRun(100, func() { p = c.dropPolicy(m, &named) })
		if allocs != 0 {
			t.Fatalf("dropPolicy allocates %.1f times", allocs)
		}
	})
}

func TestDropAxisWarnings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := policyCore(t, func(cfg *domain.Config) {
			cfg.Layout.Overflow = "scroll"
			// Scroll layout: left/right scroll, top/bottom switch workspace.
			cfg.Drop.SetEdge(domain.DropSideLeft, domain.DropWorkspace) // invalid
			cfg.Drop.SetEdge(domain.DropSideTop, domain.DropWorkspace)  // valid
			cfg.Drop.SetEdge(domain.DropSideBottom, domain.DropScroll)  // invalid
			cfg.Drop.SetEdge(domain.DropSideRight, domain.DropAuto)     // never invalid
			var out domain.DropPolicy
			out.SetEdge(domain.DropSideRight, domain.DropWorkspace) // invalid
			cfg.Drop.Outputs = []domain.OutputDrop{{Output: "OUT", DropPolicy: out}}
		})
		c.cur().mon.Name = "OUT"
		got := c.dropAxisWarnings(c.cfg)
		want := []string{
			"drop.edge.left: workspace does nothing on this edge in the scroll layout (zone removed)",
			"drop.edge.bottom: scroll does nothing on this edge in the scroll layout (zone removed)",
			"drop.OUT.edge.right: workspace does nothing on this edge in the scroll layout (zone removed)",
		}
		if !slices.Equal(got, want) {
			t.Fatalf("warnings:\n%q\nwant\n%q", got, want)
		}
		// Two screens on the same layout warn once per key.
		c.screens = append(c.screens, c.screens[0])
		if got := c.dropAxisWarnings(c.cfg); len(got) != 3 {
			t.Fatalf("%d warnings: %q", len(got), got)
		}
	})
}

func TestDropAxisWarningSentOnApply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := dragCore(t)
		errs := make(chan error, 8)
		c.ch.ConfigErrors = errs
		cfg := c.cfg
		cfg.Layout.Overflow = "scroll"
		cfg.Drop.SetEdge(domain.DropSideLeft, domain.DropWorkspace)
		if err := c.apply(cfg); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-errs:
			w, ok := errors.AsType[domain.ConfigWarning](err)
			if !ok || w.Component != "drag" || !strings.HasPrefix(w.Msg, "drop.edge.left: ") {
				t.Fatalf("%#v", err)
			}
		default:
			t.Fatal("no warning")
		}
		if len(errs) != 0 {
			t.Fatalf("%d more", len(errs))
		}
		// The config is in effect.
		if !c.cfg.Drop.EdgeSet(domain.DropSideLeft) {
			t.Fatal("config not applied")
		}
	})
}
