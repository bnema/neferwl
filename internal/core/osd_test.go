package core

import (
	"context"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestOSDPinAction(t *testing.T) {
	c := &Core{}
	c.applyAction(ActionOSDPin)
	if c.osd.pinned || c.osd.shown {
		t.Fatal("disabled OSD pinned")
	}
	c.cfg.OSD.Enable = true
	c.applyAction(ActionOSDPin)
	if !c.osd.pinned || !c.osd.shown || !c.osd.key.pinned() {
		t.Fatal("pin did not show")
	}
	c.osdApply(osdHide)
	if !c.osd.shown {
		t.Fatal("release hid pinned panel")
	}
	c.applyAction(ActionOSDPin)
	if c.osd.pinned || c.osd.shown {
		t.Fatal("unpin did not hide")
	}
	c.security.Protected = true
	c.applyAction(ActionOSDPin)
	if c.osd.pinned || c.osd.shown {
		t.Fatal("protected session pinned")
	}
}

func TestOSDWatchAfterUnmapClearsContent(t *testing.T) {
	commands := make(chan ports.ClientCommand, 2)
	c := &Core{ch: Channels{Commands: commands}}
	c.cfg.OSD.Enable = true
	c.osd.shown, c.osd.pinned = true, true
	if err := c.osdWatch(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	<-commands
	c.osdPanel(ports.WindowStats{Window: 1, ClientFPS: 144})
	// WindowUnmapped clears the window before publishing the empty focus.
	c.osd.window = 0
	if err := c.osdWatch(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if c.osd.window != 0 || !c.osd.shown || !c.osd.pinned || c.osd.panel.Lines != [4]string{"", "waiting for frames..."} {
		t.Fatalf("unmapped watch retained content: %+v", c.osd)
	}
	select {
	case cmd := <-commands:
		if cmd != (ports.WatchStats{Window: 0}) {
			t.Fatalf("command: %+v", cmd)
		}
	default:
		t.Fatal("unmapped watch did not stop stats")
	}
}
