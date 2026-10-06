package core

import (
	"context"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// A neighbor workspace is visible only as an overview preview; its real
// placement on the output is hidden. Its last configure must not be resized.
func TestOverviewNeighborPreviewKeepsHiddenConfigureSize(t *testing.T) {
	cfg := ports.Config{}
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 3
	commands := make(chan ports.ClientCommand, 256)
	scenes := make(chan []ports.Scene, 1)
	c, err := New(cfg, Channels{Commands: commands, Scenes: scenes}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c.addScreen(ports.OutputInfo{Name: "OUT-1", Width: 600, Height: 400})
	m := c.cur().mon
	m.AddWindow(1)
	m.Focus(1)
	m.AddWindow(2)
	ctx := context.Background()
	publish := func() []ports.ConfigureWindow {
		t.Helper()
		if err := c.publish(ctx); err != nil {
			t.Fatal(err)
		}
		<-scenes
		var sent []ports.ConfigureWindow
		for len(commands) > 0 {
			if v, ok := (<-commands).(ports.ConfigureWindow); ok {
				sent = append(sent, v)
			}
		}
		return sent
	}
	publish() // establish a sized configure while workspace 2 is on screen
	before := c.configures.sent[2]
	if before.Width == 0 || before.Height == 0 {
		t.Fatalf("missing prior size: %+v", before)
	}
	m.Focus(0)
	publish() // the workspace is hidden on the output
	if c.configures.sent[2].Visible {
		t.Fatal("neighbor was not hidden before overview")
	}
	m.ToggleOverview()
	preview := previewOf(t, m.Layout(), 2)
	if preview.Hidden || preview.Preview <= 0 {
		t.Fatalf("neighbor not shown as preview: %+v", preview)
	}
	for _, v := range publish() {
		if v.ID == 2 && (v.Width != before.Width || v.Height != before.Height) {
			t.Fatalf("hidden neighbor resized from %+v to %+v", before, v)
		}
	}
	got := c.configures.sent[2]
	if got.Width != before.Width || got.Height != before.Height {
		t.Fatalf("neighbor size from %+v to %+v", before, got)
	}
}

func TestOverviewConfigureUsesRealLayoutAfterMaximize(t *testing.T) {
	cfg := ports.Config{}
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.Overflow = "fixed"
	cfg.Layout.MaxColumns = 3
	commands := make(chan ports.ClientCommand, 256)
	scenes := make(chan []ports.Scene, 1)
	c, err := New(cfg, Channels{Commands: commands, Scenes: scenes}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c.addScreen(ports.OutputInfo{Name: "OUT-1", Width: 600, Height: 400})
	m := c.cur().mon
	for id := WindowID(1); id <= 3; id++ {
		m.AddWindow(id)
	}
	ctx := context.Background()
	publish := func() []ports.ConfigureWindow {
		t.Helper()
		if err := c.publish(ctx); err != nil {
			t.Fatal(err)
		}
		<-scenes
		var out []ports.ConfigureWindow
		for len(commands) > 0 {
			if v, ok := (<-commands).(ports.ConfigureWindow); ok {
				out = append(out, v)
			}
		}
		return out
	}
	publish()
	before := c.configures.sent[3]
	m.ToggleOverview()
	publish()
	m.Current().ToggleFullWidth() // a real workspace mutation, not an overview bind
	real := previewOf(t, m.Current().Layout(), 3)
	expected := c.clientRect(real)
	ps := m.Layout()
	preview := previewOf(t, ps, 3)
	if preview.Preview == 0 || expected.W <= before.Width {
		t.Fatalf("setup before %+v expected %+v preview %+v", before, expected, preview)
	}
	got := publish()
	found := false
	for _, v := range got {
		if v.ID == 3 {
			found = true
			if v.Width != expected.W || v.Height != expected.H {
				t.Fatalf("preview sized configure %+v want %dx%d", v, expected.W, expected.H)
			}
		}
	}
	if !found {
		t.Fatalf("no immediate resize while overview open; configures %v", got)
	}
}
