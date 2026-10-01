package core

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// groupCore has outputs A and B; A (its home) shows named workspace s, whose window 1
// is fullscreen on its own sibling workspace (fixed overflow).
func groupCore(t *testing.T) (c *Core, named, fs *Workspace) {
	t.Helper()
	var cfg ports.Config
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 2
	cfg.Layout.Overflow = string(OverflowFixed)
	cfg.Workspaces = []ports.WorkspaceConfig{{Name: "s", Monitor: "A"}}
	c = newSizedCore(t, cfg)
	c.addScreen(ports.OutputInfo{Name: "A", Width: 300, Height: 200})
	c.addScreen(ports.OutputInfo{Name: "B", Width: 600, Height: 400})
	a := c.screens[0]
	a.mon.ToggleNamed("s")
	named = a.mon.Current()
	a.mon.AddWindow(1)
	a.mon.AddWindow(2)
	a.mon.ToggleFullscreen()
	fs = a.mon.Current()
	if fs.origin != named {
		t.Fatal("test did not create fullscreen sibling")
	}
	return c, named, fs
}

func screenNamed(t *testing.T, c *Core, name string) *screen {
	t.Helper()
	if i := c.screenIndex(name); i >= 0 {
		return c.screens[i]
	}
	t.Fatalf("no screen %s", name)
	return nil
}

func TestUnplugKeepsFullscreenSiblingLinked(t *testing.T) {
	c, named, fs := groupCore(t)
	c.removeScreen("A")
	b := screenNamed(t, c, "B")
	if !b.mon.has(named) || !b.mon.has(fs) || fs.origin != named {
		t.Fatalf("sibling unlinked on unplug: origin %p named %p", fs.origin, named)
	}
}

func TestReplugReturnsFullscreenGroupTogether(t *testing.T) {
	c, named, fs := groupCore(t)
	c.removeScreen("A")
	c.addScreen(ports.OutputInfo{Name: "A", Width: 300, Height: 200})
	c.settleGuests()
	a := screenNamed(t, c, "A")
	if !a.mon.has(named) || !a.mon.has(fs) || fs.origin != named {
		t.Fatal("group split on replug")
	}
}

// A group with a member on screen stays until the user leaves it, then
// returns whole.
func TestReplugKeepsShownFullscreenGroup(t *testing.T) {
	c, named, fs := groupCore(t)
	c.removeScreen("A")
	b := screenNamed(t, c, "B")
	b.mon.show(fs)
	c.addScreen(ports.OutputInfo{Name: "A", Width: 300, Height: 200})
	c.settleGuests()
	if !b.mon.has(named) || !b.mon.has(fs) || fs.origin != named {
		t.Fatal("shown group split on replug")
	}
	b.mon.Focus(len(b.mon.Workspaces) - 1) // the trailing empty workspace
	c.settleGuests()
	a := screenNamed(t, c, "A")
	if !a.mon.has(named) || !a.mon.has(fs) || fs.origin != named {
		t.Fatal("group did not return once left")
	}
}

func TestMoveWorkspaceTakesFullscreenGroup(t *testing.T) {
	c, named, fs := groupCore(t)
	c.focusScreen = 0
	c.moveWorkspace(1)
	b := screenNamed(t, c, "B")
	if !b.mon.has(named) || !b.mon.has(fs) || fs.origin != named || b.mon.Current() != fs || c.focusScreen != 1 {
		t.Fatal("move-workspace split the group")
	}
}

func TestMonitorAllAllocations(t *testing.T) {
	c, _, _ := groupCore(t)
	m := c.screens[0].mon
	if n := testing.AllocsPerRun(100, func() { m.fullscreenHome() }); n != 0 {
		t.Fatalf("%v allocs", n)
	}
}

// A sibling made while its origin was a guest returns next to it, after
// the home's own numbered workspaces, and shares the origin's home.
func TestReplugPlacesSiblingNextToOrigin(t *testing.T) {
	var cfg ports.Config
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 2
	cfg.Layout.Overflow = string(OverflowFixed)
	cfg.Workspaces = []ports.WorkspaceConfig{{Name: "s", Monitor: "A"}}
	c := newSizedCore(t, cfg)
	c.addScreen(ports.OutputInfo{Name: "A", Width: 300, Height: 200})
	c.addScreen(ports.OutputInfo{Name: "B", Width: 600, Height: 400})
	a := screenNamed(t, c, "A")
	a.mon.AddWindow(5)
	own := a.mon.Current()
	c.removeScreen("A")
	b := screenNamed(t, c, "B")
	b.mon.Focus(len(b.mon.Workspaces) - 1)
	anchor := b.mon.Current()
	b.mon.ToggleNamed("s")
	named := b.mon.Current()
	b.mon.AddWindow(1)
	b.mon.AddWindow(2)
	b.mon.ToggleFullscreen()
	fs := b.mon.Current()
	if fs.origin != named || fs.home != "" {
		t.Fatalf("setup: origin %v home %q", fs.origin == named, fs.home)
	}
	c.addScreen(ports.OutputInfo{Name: "A", Width: 300, Height: 200})
	b.mon.show(anchor)
	c.settleGuests()
	a = screenNamed(t, c, "A")
	if !a.mon.has(named) || fs.origin != named || indexOf(a.mon.Workspaces, fs) <= indexOf(a.mon.Workspaces, own) {
		t.Fatalf("own %d sibling %d linked %v", indexOf(a.mon.Workspaces, own), indexOf(a.mon.Workspaces, fs), fs.origin == named)
	}
}

// moveWorkspace from the origin itself takes its sibling and keeps it
// right after the origin.
func TestMoveWorkspaceFromNumberedOrigin(t *testing.T) {
	var cfg ports.Config
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 2
	cfg.Layout.Overflow = string(OverflowFixed)
	c := newSizedCore(t, cfg)
	c.addScreen(ports.OutputInfo{Name: "A", Width: 300, Height: 200})
	c.addScreen(ports.OutputInfo{Name: "B", Width: 600, Height: 400})
	a, b := screenNamed(t, c, "A"), screenNamed(t, c, "B")
	b.mon.AddWindow(9)
	a.mon.AddWindow(1)
	a.mon.AddWindow(2)
	origin := a.mon.Current()
	a.mon.ToggleFullscreen()
	fs := a.mon.Current()
	a.mon.show(origin)
	c.focusScreen = 0
	c.moveWorkspace(1)
	if fs.origin != origin || b.mon.Current() != origin || indexOf(b.mon.Workspaces, fs) != indexOf(b.mon.Workspaces, origin)+1 {
		t.Fatalf("origin %d sibling %d linked %v", indexOf(b.mon.Workspaces, origin), indexOf(b.mon.Workspaces, fs), fs.origin == origin)
	}
}

// A hidden origin attached to a returning numbered workspace gets its
// sibling right after that anchor, as enterFullscreen places it.
func TestReplugPlacesSiblingAfterHiddenOriginAnchor(t *testing.T) {
	var cfg ports.Config
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 2
	cfg.Layout.Overflow = string(OverflowFixed)
	cfg.Workspaces = []ports.WorkspaceConfig{{Name: "s", Monitor: "A"}}
	c := newSizedCore(t, cfg)
	c.addScreen(ports.OutputInfo{Name: "A", Width: 300, Height: 200})
	c.addScreen(ports.OutputInfo{Name: "B", Width: 600, Height: 400})
	a := screenNamed(t, c, "A")
	a.mon.AddWindow(5)
	a.mon.Focus(1)
	a.mon.AddWindow(6)
	anchor := a.mon.Current()
	// A later workspace, so that appending would not land after the anchor.
	a.mon.Focus(2)
	a.mon.AddWindow(7)
	a.mon.show(anchor)
	a.mon.ToggleNamed("s")
	named := a.mon.Current()
	a.mon.AddWindow(1)
	a.mon.AddWindow(2)
	a.mon.ToggleFullscreen()
	fs := a.mon.Current()
	if fs.origin != named || named.overviewAfter != anchor {
		t.Fatal("setup")
	}
	c.removeScreen("A")
	b := screenNamed(t, c, "B")
	b.mon.Focus(len(b.mon.Workspaces) - 1)
	c.addScreen(ports.OutputInfo{Name: "A", Width: 300, Height: 200})
	c.settleGuests()
	a = screenNamed(t, c, "A")
	if fs.origin != named || named.overviewAfter != anchor || indexOf(a.mon.Workspaces, fs) != indexOf(a.mon.Workspaces, anchor)+1 {
		t.Fatalf("anchor %d sibling %d linked %v", indexOf(a.mon.Workspaces, anchor), indexOf(a.mon.Workspaces, fs), fs.origin == named)
	}
}
