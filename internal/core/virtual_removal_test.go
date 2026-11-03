package core

import (
	"testing"

	"github.com/bnema/neferwl/internal/domain"
)

func TestVirtualRemovalForgetsHomes(t *testing.T) {
	c := layoutCore(0, tile{"DP-1", 0, 0, 640, 360}, tile{"tv", 2664, 0, 1280, 720})
	c.screens[1].info.Virtual = true
	host, gone := c.screens[0].mon, c.screens[1].mon
	numbered := gone.Current()
	numbered.AddWindow(1)
	numbered.Home, numbered.HomePos = "other", 7
	hidden := workspace()
	hidden.Name = "hidden"
	hidden.AddWindow(2)
	gone.adopt(hidden, true, 0)
	guest := workspace()
	guest.AddWindow(3)
	guest.Home, guest.HomePos = "tv", 5
	host.adopt(guest, false, len(host.Workspaces))
	c.removeScreen("tv")
	for _, w := range []*Workspace{numbered, hidden, guest} {
		if w.Home != "" || w.HomePos != 0 {
			t.Fatalf("home retained: %q %d", w.Home, w.HomePos)
		}
	}
	if w, _ := host.find(1); w != numbered {
		t.Fatal("numbered workspace not adopted")
	}
	if w, _ := host.find(2); w != hidden || !host.isHidden(w) {
		t.Fatal("hidden workspace not adopted hidden")
	}
	c.addScreen(domain.OutputInfo{Name: "tv", Virtual: true, Width: 1280, Height: 720})
	c.settleGuests()
	for _, id := range []WindowID{1, 2, 3} {
		if s, _ := c.screenOf(id); s == nil || s.name() != "DP-1" {
			t.Fatalf("window %d returned to virtual output", id)
		}
	}
}

func TestLastVirtualRemovalClearsHome(t *testing.T) {
	c := layoutCore(0, tile{"tv", 0, 0, 1280, 720})
	c.screens[0].info.Virtual = true
	w := c.cur().mon.Current()
	w.AddWindow(1)
	w.Home, w.HomePos = "tv", 9
	c.removeScreen("tv")
	if w.Home != "" || w.HomePos != 0 || c.cur().name() != "" || !w.Has(1) {
		t.Fatal("placeholder did not retain window without home")
	}
}
