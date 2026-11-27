package core

import (
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
