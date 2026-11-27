package core

import (
	"fmt"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
)

// homeCore is a core with the given outputs plugged in order.
func homeCore(t *testing.T, outs ...domain.OutputInfo) *Core {
	t.Helper()
	c := scopeCore(t, domain.StashScopeWorkspace)
	for _, o := range outs {
		o.Width, o.Height = 300, 200
		c.addScreen(o)
	}
	return c
}

func screenNamed(c *Core, name string) *screen {
	if i := c.screenIndex(name); i >= 0 {
		return c.screens[i]
	}
	return nil
}

func TestResolveHome(t *testing.T) {
	laptop := domain.OutputInfo{Name: "eDP-1", EDIDID: "aa01", EDIDHash: "ha"}
	ext := domain.OutputInfo{Name: "DP-7", EDIDID: "bb01", EDIDHash: "h1"}
	bare := domain.OutputInfo{Name: "DP-5"}
	t1 := domain.OutputInfo{Name: "DP-1", EDIDID: "dd01", EDIDHash: "h"}
	t2 := domain.OutputInfo{Name: "DP-2", EDIDID: "dd01", EDIDHash: "h"}
	cousin := domain.OutputInfo{Name: "DP-3", EDIDID: "dd01", EDIDHash: "other"}
	virt := domain.OutputInfo{Name: "HEADLESS-2", Virtual: true}
	for _, tc := range []struct {
		name string
		outs []domain.OutputInfo
		ref  domain.MonitorRef
		want string // "" for nil
	}{
		{"unique id on another connector", []domain.OutputInfo{laptop, ext}, domain.MonitorRef{Connector: "DP-5", EDIDID: "bb01", EDIDHash: "h1"}, "DP-7"},
		{"unique id, other hash", []domain.OutputInfo{laptop, ext}, domain.MonitorRef{Connector: "DP-5", EDIDID: "bb01", EDIDHash: "zz"}, "DP-7"},
		{"no id on the ref, same connector", []domain.OutputInfo{laptop, ext}, domain.MonitorRef{Connector: "DP-7"}, "DP-7"},
		{"unknown id on the screen, same connector", []domain.OutputInfo{laptop, bare}, domain.MonitorRef{Connector: "DP-5", EDIDID: "bb01", EDIDHash: "h1"}, "DP-5"},
		{"different known ids on one connector", []domain.OutputInfo{laptop, {Name: "DP-5", EDIDID: "cc01"}}, domain.MonitorRef{Connector: "DP-5", EDIDID: "bb01", EDIDHash: "h1"}, ""},
		{"no id anywhere, other connector", []domain.OutputInfo{laptop, bare}, domain.MonitorRef{Connector: "DP-9"}, ""},
		{"twins and connector", []domain.OutputInfo{laptop, t1, t2}, t2.Ref(), "DP-2"},
		{"twins, other twin by connector", []domain.OutputInfo{laptop, t1, t2}, t1.Ref(), "DP-1"},
		{"twins without matching connector", []domain.OutputInfo{laptop, t1, t2}, domain.MonitorRef{Connector: "DP-9", EDIDID: "dd01", EDIDHash: "h"}, ""},
		{"same id narrowed by hash", []domain.OutputInfo{t1, cousin}, domain.MonitorRef{Connector: "DP-9", EDIDID: "dd01", EDIDHash: "other"}, "DP-3"},
		{"same id, no hash, narrowed by connector", []domain.OutputInfo{t1, cousin}, domain.MonitorRef{Connector: "DP-3", EDIDID: "dd01"}, "DP-3"},
		{"same id, no hash, no connector", []domain.OutputInfo{t1, cousin}, domain.MonitorRef{Connector: "DP-9", EDIDID: "dd01"}, ""},
		{"virtual without id is never a candidate", []domain.OutputInfo{laptop, virt}, domain.MonitorRef{Connector: "HEADLESS-2"}, ""},
		{"virtual with id is a candidate", []domain.OutputInfo{laptop, {Name: "ext", Virtual: true, EDIDID: "ee01"}}, domain.MonitorRef{Connector: "ext2", EDIDID: "ee01"}, "ext"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := homeCore(t, tc.outs...)
				got := c.resolveHome(tc.ref)
				if tc.want == "" {
					if got != nil {
						t.Fatalf("resolveHome(%+v) = %s, want nil", tc.ref, got.name())
					}
					return
				}
				if got == nil || got.name() != tc.want {
					t.Fatalf("resolveHome(%+v) = %v, want %s", tc.ref, got, tc.want)
				}
			})
		})
	}
}

// Unplugging twin t2 while t1 stays never resolves t2's ref to t1: the
// fingerprint was seen twice, so it requires its connector from then on.
func TestResolveHomeTwinMemory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t1 := domain.OutputInfo{Name: "DP-1", EDIDID: "dd01", EDIDHash: "h"}
		t2 := domain.OutputInfo{Name: "DP-2", EDIDID: "dd01", EDIDHash: "h"}
		c := homeCore(t, domain.OutputInfo{Name: "eDP-1"}, t1, t2)
		c.removeScreen("DP-2")
		if got := c.resolveHome(t2.Ref()); got != nil {
			t.Fatalf("t2 ref resolved to %s, want nil", got.name())
		}
		if got := c.resolveHome(t1.Ref()); got != screenNamed(c, "DP-1") {
			t.Fatalf("t1 ref resolved to %v, want DP-1", got)
		}
		c.addScreen(domain.OutputInfo{Name: "DP-3", EDIDID: "dd01", EDIDHash: "h", Width: 300, Height: 200})
		if got := c.resolveHome(t2.Ref()); got != nil {
			t.Fatalf("t2 ref resolved to %s on another connector, want nil", got.name())
		}
	})
}

// Without twins seen together, a single monitor is found on any connector.
func TestResolveHomeNoTwinMemory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ext := domain.OutputInfo{Name: "DP-5", EDIDID: "bb01", EDIDHash: "h1"}
		c := homeCore(t, domain.OutputInfo{Name: "eDP-1"}, ext)
		c.removeScreen("DP-5")
		c.addScreen(domain.OutputInfo{Name: "DP-7", EDIDID: "bb01", EDIDHash: "h1", Width: 300, Height: 200})
		if got := c.resolveHome(ext.Ref()); got != screenNamed(c, "DP-7") {
			t.Fatalf("resolved to %v, want DP-7", got)
		}
	})
}

func TestHomeOf(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := homeCore(t, domain.OutputInfo{Name: "eDP-1"}, domain.OutputInfo{Name: "DP-7", EDIDID: "bb01"})
		w := &Workspace{}
		if got := c.homeOf(w); got != nil {
			t.Fatalf("no homes: %s", got.name())
		}
		w.Homes = []domain.MonitorRef{{Connector: "DP-9", EDIDID: "zz"}, {Connector: "DP-5", EDIDID: "bb01"}}
		if got := c.homeOf(w); got != screenNamed(c, "DP-7") {
			t.Fatalf("homes: %v, want DP-7 (first that resolves)", got)
		}
		w.HomeSpec = "eDP-1"
		if got := c.homeOf(w); got != screenNamed(c, "eDP-1") {
			t.Fatalf("spec: %v, want eDP-1", got)
		}
		w.HomeSpec = "HDMI-1"
		if got := c.homeOf(w); got != nil {
			t.Fatalf("unmatched spec: %s, want nil (spec wins over homes)", got.name())
		}
	})
}

func TestPushHome(t *testing.T) {
	a := domain.MonitorRef{Connector: "DP-1", EDIDID: "aa"}
	b := domain.MonitorRef{Connector: "DP-2", EDIDID: "bb"}
	c := domain.MonitorRef{Connector: "DP-3", EDIDID: "cc"}
	d := domain.MonitorRef{Connector: "DP-4", EDIDID: "dd"}
	e := domain.MonitorRef{Connector: "DP-5", EDIDID: "ee"}
	w := &Workspace{}
	pushHome(w, a)
	pushHome(w, b)
	if !slices.Equal(w.Homes, []domain.MonitorRef{b, a}) {
		t.Fatalf("front insert: %v", w.Homes)
	}
	a2 := a
	a2.EDIDHash = "new"
	pushHome(w, a2)
	if !slices.Equal(w.Homes, []domain.MonitorRef{a2, b}) {
		t.Fatalf("dedup by connector and id: %v", w.Homes)
	}
	pushHome(w, domain.MonitorRef{Connector: "DP-1", EDIDID: "other"})
	if len(w.Homes) != 3 {
		t.Fatalf("another id on the same connector is a distinct home: %v", w.Homes)
	}
	pushHome(w, c)
	pushHome(w, d)
	pushHome(w, e)
	if !slices.Equal(w.Homes, []domain.MonitorRef{e, d, c, {Connector: "DP-1", EDIDID: "other"}}) {
		t.Fatalf("cap %d: %v", maxHomes, w.Homes)
	}
}

func TestResolveHomeDoesNotAllocate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t1 := domain.OutputInfo{Name: "DP-1", EDIDID: "dd01", EDIDHash: "h"}
		t2 := domain.OutputInfo{Name: "DP-2", EDIDID: "dd01", EDIDHash: "h"}
		c := homeCore(t, domain.OutputInfo{Name: "eDP-1"}, t1, t2)
		w := &Workspace{Homes: []domain.MonitorRef{{Connector: "DP-9", EDIDID: "zz"}, t2.Ref()}}
		refs := []domain.MonitorRef{t1.Ref(), {Connector: "DP-9", EDIDID: "dd01", EDIDHash: "h"}, {Connector: "eDP-1"}, {Connector: "DP-5", EDIDID: "bb01"}}
		allocs := testing.AllocsPerRun(100, func() {
			for _, r := range refs {
				_ = c.resolveHome(r)
			}
			_ = c.homeOf(w)
		})
		if allocs != 0 {
			t.Fatalf("resolveHome allocates %v times", allocs)
		}
	})
}

// The last physical output leaving strands its non-empty workspaces; the
// flag clears once the home holds them again, and an ordinary unplug never
// sets it.
func TestStrandedFlagLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ext := domain.OutputInfo{Name: "DP-5", EDIDID: "bb01", EDIDHash: "h1"}
		c := homeCore(t, ext)
		m := c.cur().mon
		w := m.Current()
		w.AddWindow(1)
		c.removeScreen("DP-5")
		if !w.Stranded || !slices.Equal(w.Homes, []domain.MonitorRef{ext.Ref()}) {
			t.Fatalf("last output: stranded %t homes %v", w.Stranded, w.Homes)
		}
		for e := range m.all() {
			if e != w && e.Stranded {
				t.Fatal("empty workspace stranded")
			}
		}
		ext.Width, ext.Height = 300, 200
		c.addScreen(ext)
		c.settleGuests()
		if w.Stranded {
			t.Fatal("still stranded on its home")
		}
		c.addScreen(domain.OutputInfo{Name: "eDP-1", Width: 300, Height: 200})
		c.focusScreen = c.screenIndex("eDP-1")
		c.removeScreen("DP-5")
		if w.Stranded {
			t.Fatal("ordinary unplug stranded")
		}
	})
}

// A virtual last output strands nothing.
func TestVirtualLastOutputStrandsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := homeCore(t, domain.OutputInfo{Name: "HEADLESS-1", Virtual: true})
		w := c.cur().mon.Current()
		w.AddWindow(1)
		c.removeScreen("HEADLESS-1")
		if w.Stranded || len(w.Homes) != 0 {
			t.Fatalf("stranded %t homes %v", w.Stranded, w.Homes)
		}
	})
}

// moveWorkspace learns a physical destination as the preferred home and
// forgets homes on a virtual one.
func TestMoveWorkspaceLearnsHome(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t1 := domain.OutputInfo{Name: "DP-1", EDIDID: "dd01", EDIDHash: "h"}
		virt := domain.OutputInfo{Name: "HEADLESS-2", Virtual: true}
		c := homeCore(t, domain.OutputInfo{Name: "eDP-1"}, t1, virt)
		c.opts.PlaceVirtuals = true
		c.order()
		c.focusScreen = c.screenIndex("eDP-1")
		w := c.cur().mon.Current()
		w.AddWindow(1)
		old := domain.MonitorRef{Connector: "DP-9", EDIDID: "ee01"}
		w.Homes, w.Stranded = []domain.MonitorRef{old}, true
		c.moveWorkspace(dirRight)
		if c.cur().name() != "DP-1" {
			t.Fatalf("moved to %s, want DP-1", c.cur().name())
		}
		t1.Width, t1.Height = 300, 200
		if want := []domain.MonitorRef{t1.Ref(), old}; !slices.Equal(w.Homes, want) || w.Stranded {
			t.Fatalf("homes %v stranded %t, want %v", w.Homes, w.Stranded, want)
		}
		c.moveWorkspace(dirRight)
		if c.cur().name() != "HEADLESS-2" {
			t.Fatalf("moved to %s, want HEADLESS-2", c.cur().name())
		}
		if len(w.Homes) != 0 {
			t.Fatalf("homes %v on a virtual output", w.Homes)
		}
	})
}

func info(name, id string) domain.OutputInfo {
	return domain.OutputInfo{Name: name, EDIDID: id, Width: 300, Height: 200}
}

// C1: a stranded workspace that became empty and unnamed is not settled:
// normalize would drop it, leaving a detached workspace shown on its home.
func TestSettleSkipsEmptiedStrandedWorkspace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ext := info("DP-5", "bb01")
		c := homeCore(t, ext)
		w := c.cur().mon.Current()
		w.AddWindow(1)
		c.removeScreen("DP-5")
		c.addScreen(info("eDP-1", "aa01"))
		laptop := c.screens[c.screenIndex("eDP-1")]
		if laptop.mon.Current() != w || !w.Stranded {
			t.Fatal("setup: stranded workspace not shown on the laptop")
		}
		w.RemoveWindow(1)
		c.addScreen(ext)
		c.settleGuests()
		for _, s := range c.screens {
			if cur := s.mon.Current(); !s.mon.has(cur) {
				t.Fatalf("%s shows a detached workspace", s.name())
			}
		}
		if len(w.Homes) != 0 || w.Stranded || w.HomePos != 0 {
			t.Fatalf("emptied workspace kept homes %v stranded %t pos %d", w.Homes, w.Stranded, w.HomePos)
		}
	})
}

// C1: show never puts a workspace the monitor does not hold on screen.
func TestShowIgnoresForeignWorkspace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := homeCore(t, info("DP-1", ""))
		m := c.cur().mon
		before := m.Current()
		m.show(workspace())
		if m.Current() != before {
			t.Fatal("foreign workspace shown")
		}
	})
}

// C2: a last-output unplug keeps the order of the external's workspaces,
// including one moved there explicitly (a learned home).
func TestLastOutputUnplugKeepsWorkspaceOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		laptop, ext := info("eDP-1", "aa01"), info("DP-5", "bb01")
		c := homeCore(t, laptop, ext)
		lap, out := c.screens[c.screenIndex("eDP-1")], c.screens[c.screenIndex("DP-5")]
		native := out.mon.Current()
		native.AddWindow(1)
		out.mon.normalize()
		c.focusScreen = c.screenIndex("eDP-1")
		moved := lap.mon.Current()
		moved.AddWindow(2)
		lap.mon.normalize()
		c.moveWorkspace(dirRight)
		if idx := indexOf(out.mon.Workspaces, moved); idx != 1 || indexOf(out.mon.Workspaces, native) != 0 {
			t.Fatalf("setup: order %d %d", indexOf(out.mon.Workspaces, native), idx)
		}
		c.removeScreen("eDP-1") // lid closed
		c.removeScreen("DP-5")  // last output
		c.addScreen(laptop)
		c.settleGuests()
		c.addScreen(ext)
		c.settleGuests()
		out = c.screens[c.screenIndex("DP-5")]
		if indexOf(out.mon.Workspaces, native) != 0 || indexOf(out.mon.Workspaces, moved) != 1 {
			t.Fatalf("order after return: native %d moved %d", indexOf(out.mon.Workspaces, native), indexOf(out.mon.Workspaces, moved))
		}
	})
}

// C3: a known monitor back on its connector with an unreadable EDID keeps
// the ref's identity, so another known monitor there later takes nothing.
func TestUnreadableReplugKeepsIdentity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ext := info("DP-5", "bb01")
		c := homeCore(t, info("eDP-1", "aa01"), ext)
		c.focusScreen = c.screenIndex("DP-5")
		w := c.cur().mon.Current()
		w.AddWindow(1)
		c.focusScreen = c.screenIndex("eDP-1")
		c.removeScreen("DP-5")
		c.addScreen(info("DP-5", ""))
		c.settleGuests()
		if s, _ := c.screenOf(1); s.name() != "DP-5" {
			t.Fatalf("not home: %s", s.name())
		}
		if want := (domain.MonitorRef{Connector: "DP-5", EDIDID: "bb01"}); len(w.Homes) != 1 || w.Homes[0] != want {
			t.Fatalf("homes %v, want [%v]", w.Homes, want)
		}
		c.removeScreen("DP-5")
		c.addScreen(info("DP-5", "cc01"))
		c.settleGuests()
		if s, _ := c.screenOf(1); s.name() != "eDP-1" {
			t.Fatalf("another monitor took it: %s", s.name())
		}
	})
}

// C3: the first output back is the home itself, on a new connector: the
// ref follows it, so a later unreadable replug there finds it.
func TestStrandedAtHomeRefreshesConnector(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := homeCore(t, info("DP-5", "bb01"))
		w := c.cur().mon.Current()
		w.AddWindow(1)
		c.removeScreen("DP-5")
		c.addScreen(info("DP-7", "bb01"))
		c.settleGuests()
		if len(w.Homes) != 1 || w.Homes[0].Connector != "DP-7" || w.Stranded {
			t.Fatalf("homes %v stranded %t", w.Homes, w.Stranded)
		}
		c.addScreen(info("eDP-1", "aa01"))
		c.focusScreen = c.screenIndex("eDP-1")
		c.removeScreen("DP-7")
		c.addScreen(info("DP-7", ""))
		c.settleGuests()
		if s, _ := c.screenOf(1); s.name() != "DP-7" {
			t.Fatalf("unreadable replug on DP-7: window on %s", s.name())
		}
	})
}

// C4: arriving at a configured home keeps the config authoritative: an
// older learned home never wins over workspace.<name>.monitor afterwards.
func TestConfiguredHomeStaysAuthoritative(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := homeCore(t, info("eDP-1", "aa01"), info("DP-5", "bb01"))
		w := workspace()
		w.Name = "web"
		w.AddWindow(1)
		c.screens[c.screenIndex("eDP-1")].mon.adopt(w, true, 0)
		w.Homes = []domain.MonitorRef{{Connector: "eDP-1", EDIDID: "aa01"}}
		w.HomeSpec = "DP-5"
		c.settleGuests()
		if s, _ := c.screenOf(1); s.name() != "DP-5" {
			t.Fatalf("not on its configured home: %s", s.name())
		}
		if w.HomeSpec != "DP-5" || len(w.Homes) != 0 {
			t.Fatalf("spec %q homes %v", w.HomeSpec, w.Homes)
		}
		c.settleGuests()
		if s, _ := c.screenOf(1); s.name() != "DP-5" {
			t.Fatalf("learned home won: %s", s.name())
		}
	})
}

// C4: another monitor showing up on the connector of a named workspace's
// configured home makes it a guest again, so it reaches its monitor later.
func TestKeyChangeReappliesNamedHomes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cfg domain.Config
		cfg.Keyboard.CmdKey = "alt"
		cfg.Layout.MaxColumns = 2
		cfg.Workspaces = []domain.WorkspaceConfig{{Name: "web", Monitor: "Acme X 1"}}
		c, err := New(cfg, Channels{Scenes: make(chan []domain.Scene, 1)}, testOptions(t, Options{}))
		if err != nil {
			t.Fatal(err)
		}
		acme := domain.OutputInfo{Name: "DP-2", Make: "Acme", Model: "X", Serial: "1", Width: 300, Height: 200}
		c.addScreen(acme) // web is created on its home: no HomeSpec
		c.addScreen(domain.OutputInfo{Name: "DP-1", Width: 300, Height: 200})
		s, w := c.byName("web")
		if s.name() != "DP-2" || w.HomeSpec != "" {
			t.Fatalf("web on %s spec %q", s.name(), w.HomeSpec)
		}
		w.AddWindow(1)
		c.addScreen(domain.OutputInfo{Name: "DP-2", Make: "Other", Model: "Y", Serial: "2", Width: 300, Height: 200})
		if w.HomeSpec != "Acme X 1" {
			t.Fatalf("spec %q after another monitor took DP-2", w.HomeSpec)
		}
		// The user leaves web (not on screen): it may go home.
		if s, _ := c.byName("web"); s.mon.Current() == w {
			s.mon.Focus(len(s.mon.Workspaces) - 1)
		}
		acme.Name = "DP-3"
		c.addScreen(acme)
		c.settleGuests()
		if s, _ := c.byName("web"); s.name() != "DP-3" {
			t.Fatalf("web on %s, want DP-3", s.name())
		}
	})
}

// C5: output scope, the last physical output leaves with its stash; it
// returns with that output whether another one or itself comes first.
func TestLastOutputStashReturns(t *testing.T) {
	for _, first := range []string{"other", "home"} {
		t.Run(first, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := scopeCore(t, domain.StashScopeOutput, "B")
				c.screens[0].info.EDIDID = "bb01"
				b := c.screens[0].mon
				b.AddWindow(3)
				stashTile(b.Current(), 4)
				c.lendStashes()
				c.removeScreen("B")
				c.lendStashes()
				if first == "other" {
					a := plugAs(c, domain.OutputInfo{Name: "A"})
					b = plugAs(c, domain.OutputInfo{Name: "B", EDIDID: "bb01"})
					if got := seenStash(b.Current()); !slices.Equal(got, []WindowID{4}) || b.Current().StashShown() {
						t.Fatalf("b %v shown %t", got, b.Current().StashShown())
					}
					if a.Current().StashLen() != 0 {
						t.Fatalf("a kept %v", seenStash(a.Current()))
					}
					return
				}
				b = plugAs(c, domain.OutputInfo{Name: "B", EDIDID: "bb01"})
				if got := seenStash(b.Current()); !slices.Equal(got, []WindowID{4}) || !b.Current().StashShown() {
					t.Fatalf("b %v shown %t", got, b.Current().StashShown())
				}
				// Home already: no tag is left, so nothing matches any monitor.
				tagged := false
				b.stash.ClearHome(func(domain.MonitorRef) bool { tagged = true; return false })
				if tagged {
					t.Fatal("stash window still tagged at home")
				}
			})
		})
	}
}

// C6: a pending fullscreen member returned into a stash already lent and
// shown covers the lendee (same-connector identity change).
func TestReturnedStashFullscreenCoversLendee(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, a := outputRigIdentified(t)
		c.screens[1].mon.Current().SetFullscreen(5, true)
		c.removeScreen("B")
		c.lendStashes()
		if w := a.Current(); w.Fullscreen != 5 {
			t.Fatalf("setup: a fullscreen %d", w.Fullscreen)
		}
		b := plugAs(c, domain.OutputInfo{Name: "B", EDIDID: "cc01"})
		stashTile(b.Current(), 6)
		c.lendStashes()
		if !b.Current().StashShown() || c.screens[c.screenIndex("B")].mon.lendee == nil {
			t.Fatal("setup: b stash not lent and shown")
		}
		b = plugAs(c, domain.OutputInfo{Name: "B", EDIDID: "bb01", EDIDHash: "h1"})
		w := b.Current()
		if got := seenStash(w); !slices.Equal(got, []WindowID{6, 4, 5}) {
			t.Fatalf("b stash %v", got)
		}
		if w.Fullscreen != 5 {
			t.Fatalf("lendee fullscreen %d, want 5", w.Fullscreen)
		}
	})
}

// C8: a settle that moves nothing does not allocate, with several homes
// resolved across three screens.
func TestSettleGuestsNoMoveDoesNotAllocate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := homeCore(t, info("eDP-1", "aa01"), info("DP-1", "dd01"), info("DP-2", "ee01"))
		for i, s := range c.screens {
			w := s.mon.Current()
			w.AddWindow(WindowID(i + 1))
			w.Homes = []domain.MonitorRef{{Connector: "DP-9", EDIDID: "zz"}, s.info.Ref()}
			s.mon.normalize()
		}
		// A guest the user looks at stays: still no move.
		g := c.screens[0].mon.Current()
		g.Homes = []domain.MonitorRef{c.screens[1].info.Ref()}
		c.settleGuests()
		allocs := testing.AllocsPerRun(100, c.settleGuests)
		if allocs != 0 {
			t.Fatalf("settleGuests allocates %v times", allocs)
		}
		if s, _ := c.screenOf(1); s != c.screens[0] {
			t.Fatal("shown guest moved")
		}
	})
}

// namedCore is a core with one named workspace web configured on monitor.
func namedCore(t *testing.T, monitor string) *Core {
	t.Helper()
	var cfg domain.Config
	cfg.Keyboard.CmdKey = "alt"
	cfg.Layout.MaxColumns = 2
	cfg.Workspaces = []domain.WorkspaceConfig{{Name: "web", Monitor: monitor}}
	c, err := New(cfg, Channels{Scenes: make(chan []domain.Scene, 1)}, testOptions(t, Options{}))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// R1: web arrives in place on its configured B (the placeholder held it):
// an older learned home A must not pull it away when A comes back.
func TestNamedInPlaceArrivalForgetsLearnedHome(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := namedCore(t, "B")
		a, b, other := info("A", "aa01"), info("B", "bb01"), info("C", "cc01")
		c.addScreen(b)
		c.addScreen(a)
		s, w := c.byName("web")
		if s.name() != "B" {
			t.Fatalf("setup: web on %s", s.name())
		}
		w.AddWindow(1)
		c.focusScreen = c.screenIndex("B")
		s.mon.show(w)
		c.moveWorkspace(dirRight) // explicit, onto A (right of B): learns A
		if s, _ := c.byName("web"); s.name() != "A" || len(w.Homes) == 0 || w.Homes[0].Connector != "A" {
			t.Fatalf("setup: web on %s homes %v", s.name(), w.Homes)
		}
		c.removeScreen("B")
		c.addScreen(other) // named() runs: web's config home is B again
		if w.HomeSpec != "B" {
			t.Fatalf("spec %q", w.HomeSpec)
		}
		c.removeScreen("A")
		c.removeScreen("C") // the placeholder keeps web
		c.addScreen(b)      // B first: web arrives in place
		c.settleGuests()
		c.addScreen(a)
		c.settleGuests()
		if s, _ := c.byName("web"); s.name() != "B" {
			t.Fatalf("web on %s, want its configured B", s.name())
		}
		if len(w.Homes) != 0 || w.HomeSpec != "" {
			t.Fatalf("homes %v spec %q", w.Homes, w.HomeSpec)
		}
	})
}

// R2: two shown stranded guests share home E; the one the focused host
// showed is the one shown on E.
func TestSettleShowsFocusedHostsGuest(t *testing.T) {
	for _, order := range [][2]int{{0, 1}, {1, 0}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) { settleShowsFocusedHostsGuest(t, order) })
	}
}

func settleShowsFocusedHostsGuest(t *testing.T, order [2]int) {
	synctest.Test(t, func(t *testing.T) {
		e := info("E", "ee01")
		c := homeCore(t, info("A", "aa01"), info("B", "bb01"), e)
		ga, gb := workspace(), workspace()
		ga.AddWindow(1)
		gb.AddWindow(2)
		for _, g := range []*Workspace{ga, gb} {
			g.Homes, g.Stranded = []domain.MonitorRef{e.Ref()}, true
		}
		sa, sb := c.screens[c.screenIndex("A")], c.screens[c.screenIndex("B")]
		// The focused host's guest wins whatever the bucket order.
		ga.HomePos, gb.HomePos = order[0], order[1]
		sa.mon.adopt(ga, true, 0)
		sb.mon.adopt(gb, true, 0)
		sa.mon.show(ga)
		sb.mon.show(gb)
		c.focusScreen = c.screenIndex("A")
		c.settleGuests()
		se := c.screens[c.screenIndex("E")]
		if se.mon.Current() != ga {
			t.Fatalf("E shows %v, want the focused host's guest", se.mon.Current().ID)
		}
		if c.cur() != se {
			t.Fatalf("focus on %s, want E", c.cur().name())
		}
		if !se.mon.has(gb) {
			t.Fatal("other guest not adopted by E")
		}
	})
}

// R4: a shown named workspace and its numbered overview anchor return
// together to their home replugged on another connector: each exactly once,
// the anchor kept.
func TestShownNamedReturnsWithItsAnchor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := namedCore(t, "")
		ext := info("DP-5", "bb01")
		c.addScreen(ext)
		s, web := c.byName("web")
		num := s.mon.Workspaces[0]
		num.AddWindow(1)
		web.AddWindow(2)
		s.mon.normalize()
		web.OverviewAfter = num
		s.mon.show(web)
		c.removeScreen("DP-5") // last output: both stranded
		c.addScreen(info("eDP-1", "aa01"))
		c.settleGuests()
		c.addScreen(info("DP-7", "bb01")) // home back, another connector
		c.settleGuests()
		home := c.screens[c.screenIndex("DP-7")]
		count := func(w *Workspace) (n int) {
			for _, s := range c.screens {
				for x := range s.mon.all() {
					if x == w {
						n++
					}
				}
			}
			return n
		}
		if count(web) != 1 || count(num) != 1 || !home.mon.has(web) || !home.mon.has(num) {
			t.Fatalf("web %d num %d on home %t/%t", count(web), count(num), home.mon.has(web), home.mon.has(num))
		}
		if web.OverviewAfter != num {
			t.Fatal("anchor lost")
		}
		if home.mon.Current() != web {
			t.Fatal("shown named workspace not shown on its home")
		}
	})
}

// R5: the last virtual output leaving forgets a stranded flag too.
func TestLastVirtualRemovalClearsStranded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := homeCore(t, domain.OutputInfo{Name: "tv", Virtual: true})
		w := c.cur().mon.Current()
		w.AddWindow(1)
		w.Homes, w.HomePos, w.Stranded = []domain.MonitorRef{{Connector: "tv"}}, 3, true
		c.removeScreen("tv")
		if len(w.Homes) != 0 || w.HomePos != 0 || w.Stranded {
			t.Fatalf("homes %v pos %d stranded %t", w.Homes, w.HomePos, w.Stranded)
		}
	})
}
