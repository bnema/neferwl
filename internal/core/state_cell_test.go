package core

import (
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// Tiled windows report their column and row; stashed windows and native
// floats report 0, 0.
func TestStateColumnRow(t *testing.T) {
	cfg := ports.Config{}
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 1
	c, err := New(cfg, Channels{Commands: make(chan ports.ClientCommand, 16), Scenes: make(chan []ports.Scene, 1)}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c.addScreen(ports.OutputInfo{Name: "OUT-1", Width: 400, Height: 300})
	w := c.cur().mon.Current()
	w.Columns = []Column{{Windows: []WindowID{1}}, {Windows: []WindowID{65, 66}}, {Windows: []WindowID{2}}}
	w.Stash = []Float{{ID: 7}}
	w.Floats = []Float{{ID: 8}}
	want := map[WindowID][2]int{1: {1, 1}, 65: {2, 1}, 66: {2, 2}, 2: {3, 1}, 7: {0, 0}, 8: {0, 0}}
	got := map[WindowID][2]int{}
	for _, s := range c.state().Windows {
		got[s.ID] = [2]int{s.Column, s.Row}
	}
	for id, cr := range want {
		if g, ok := got[id]; !ok || g != cr {
			t.Errorf("window %d: column,row %v, want %v", id, g, cr)
		}
	}
}
