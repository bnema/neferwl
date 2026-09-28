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
		for id, vis := range want {
			v := sent[id]
			if v.Visible != vis || v.Output == "" || v.Width <= 0 {
				t.Fatalf("window %d: %+v, want visible %v", id, v, vis)
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
	w.ToggleWindowFloating()
	visible(map[WindowID]bool{1: true, 2: true})
	w.ToggleFloatingVisible()
	visible(map[WindowID]bool{1: true, 2: false})
	w.ToggleFloatingVisible()
	// Another workspace hides them all; coming back shows them.
	if len(m.Workspaces) < 2 {
		t.Fatal("no spare workspace")
	}
	m.Focus(1)
	visible(map[WindowID]bool{1: false, 2: false})
	m.Focus(0)
	visible(map[WindowID]bool{1: true, 2: true})
}
