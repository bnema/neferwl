package core

import (
	"context"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// Every window's configure says whether it is on its output: hidden
// workspaces, scrolled-off columns, maximized neighbors and hidden floats
// are not visible, and keep their size and output.
func TestConfigureVisible(t *testing.T) {
	cfg := ports.Config{}
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 1
	commands := make(chan ports.ClientCommand, 256)
	scenes := make(chan []ports.Scene, 1)
	c, err := New(cfg, Channels{Commands: commands, Scenes: scenes})
	if err != nil {
		t.Fatal(err)
	}
	c.addScreen(ports.OutputInfo{Name: "OUT-1", Width: 100, Height: 80})
	m := c.cur().mon
	w := m.Current()
	ctx := context.Background()
	sent := map[WindowID]ports.ConfigureWindow{}
	publish := func() {
		t.Helper()
		if err := c.publish(ctx); err != nil {
			t.Fatal(err)
		}
		<-scenes
		for len(commands) > 0 {
			if v, ok := (<-commands).(ports.ConfigureWindow); ok {
				sent[v.ID] = v
			}
		}
	}
	visible := func(want map[WindowID]bool) {
		t.Helper()
		publish()
		state := map[WindowID]bool{}
		for _, s := range c.state().Windows {
			state[s.ID] = s.Visible
		}
		for id, vis := range want {
			v := sent[id]
			if v.Visible != vis || v.Output == "" || v.Width <= 0 {
				t.Fatalf("window %d: %+v, want visible %v", id, v, vis)
			}
			if state[id] != vis {
				t.Fatalf("window %d: state visible %v, want %v", id, state[id], vis)
			}
		}
	}
	// One column per screen: the second scrolls the first off.
	m.AddWindow(1)
	visible(map[WindowID]bool{1: true})
	m.AddWindow(2)
	visible(map[WindowID]bool{1: false, 2: true})
	w.FocusColumn(-1)
	visible(map[WindowID]bool{1: true, 2: false})
	// A floated window is visible until the floats are hidden.
	w.FocusID(2)
	w.ToggleWindowStash()
	visible(map[WindowID]bool{1: true, 2: true})
	w.ToggleStashVisible()
	visible(map[WindowID]bool{1: true, 2: false})
	w.ToggleStashVisible()
	// Another workspace hides them all; coming back shows them.
	if len(m.Workspaces) < 2 {
		t.Fatal("no spare workspace")
	}
	m.Focus(1)
	visible(map[WindowID]bool{1: false, 2: false})
	m.Focus(0)
	visible(map[WindowID]bool{1: true, 2: true})
	// A window mapped on a hidden workspace is told it is invisible at
	// once, sized by the client.
	m.Workspaces[1].AddWindow(3)
	publish()
	if v, ok := sent[3]; !ok || v.Visible || v.Width != 0 || v.Output != "OUT-1" {
		t.Fatalf("hidden map: %+v %v", v, ok)
	}
	// A column scrolled off takes its popups off screen with it.
	c.popups[10] = &popupState{id: 10, parent: 1, mapped: true, rect: Rect{W: 5, H: 5}}
	c.popupOrder = append(c.popupOrder, 10)
	w.FocusID(1)
	if !c.visible(10) {
		t.Fatal("popup of a visible window not on screen")
	}
	m.AddWindow(4)
	if c.visible(1) || c.visible(10) {
		t.Fatal("popup of a scrolled-off window still on screen")
	}
}
