package core

import (
	"context"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestOverviewConfigureUsesRealLayoutAfterMaximize(t *testing.T) {
	cfg := ports.Config{}
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.Overflow = "fixed"
	cfg.Layout.MaxColumns = 3
	commands := make(chan ports.ClientCommand, 256)
	scenes := make(chan []ports.Scene, 1)
	c, err := New(cfg, Channels{Commands: commands, Scenes: scenes})
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
	m.Apply(ActionMaximizeColumn)
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
