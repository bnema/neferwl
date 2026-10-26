package core

import (
	"context"
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestOSDPinAction(t *testing.T) {
	c, _ := flashCore(t)
	c.cfg.OSD.Enable = false
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
	c.osd.window = 1
	if err := c.osdWatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-commands
	c.osdPanel(ports.WindowStats{Window: 1, ClientFPS: 144})
	// WindowUnmapped clears the window before publishing the empty focus.
	c.osd.window = 0
	if err := c.osdWatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.osd.window != 0 || c.osd.shown || c.osd.pinned || c.osd.panel != nil {
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

func TestOSDScanoutLineOmitsEmptyReason(t *testing.T) {
	c := &Core{}
	for _, tc := range []struct {
		stats ports.WindowStats
		want  string
	}{
		{ports.WindowStats{Scanout: true}, "scanout on / VRR"},
		{ports.WindowStats{ScanoutReason: "overlays"}, "scanout off (overlay) / VRR"},
		{ports.WindowStats{ScanoutReason: "format"}, "scanout off format / VRR"},
	} {
		c.osdPanel(tc.stats)
		if got := c.osd.panel.Lines[1]; !strings.HasPrefix(got, tc.want) {
			t.Fatalf("line %q, want prefix %q", got, tc.want)
		}
	}
}

func TestOSDWatchKeepsTarget(t *testing.T) {
	commands := make(chan ports.ClientCommand, 2)
	c := &Core{ch: Channels{Commands: commands}}
	c.cfg.OSD.Enable = true
	c.osd.shown, c.osd.pinned = true, true
	c.osd.window, c.osd.watched = 1, 1
	if err := c.osdWatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.osd.window != 1 || !c.osd.shown || !c.osd.pinned {
		t.Fatal(c.osd)
	}
	select {
	case cmd := <-commands:
		t.Fatalf("unexpected watch: %+v", cmd)
	default:
	}
}

func TestOSDPinWithoutFocusDoesNothing(t *testing.T) {
	c, _ := indicatorCore(t)
	c.cfg.OSD.Enable = true
	if c.osdApply(osdTogglePin) || c.osd.shown || c.osd.pinned || c.osd.window != 0 {
		t.Fatal("pin without focus changed OSD", c.osd)
	}
}
