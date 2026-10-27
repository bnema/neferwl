package core

import (
	"context"
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
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
		{ports.WindowStats{ScanoutReason: "overlays"}, "scanout on (OSD hidden) / VRR"},
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

func TestOSDTearingLineSeparatesAskedFromTorn(t *testing.T) {
	c := &Core{}
	for _, tc := range []struct {
		stats ports.WindowStats
		want  string
	}{
		{ports.WindowStats{}, "tearing off /"},
		{ports.WindowStats{Tearing: true}, "tearing asked /"},
		{ports.WindowStats{Tearing: true, Async: true}, "tearing on /"},
	} {
		c.osdPanel(tc.stats)
		if got := c.osd.panel.Lines[1]; !strings.Contains(got, tc.want) {
			t.Fatalf("line %q, want %q", got, tc.want)
		}
	}
}

func TestOSDDrawnOnlyWhereTheTargetIsDrawn(t *testing.T) {
	out := ports.Scene{OutputWidth: 1920, OutputHeight: 1080}
	shown := ports.SceneWindow{ID: 7, Rect: domain.Rect{W: 800, H: 600}}
	for name, tc := range map[string]struct {
		windows []ports.SceneWindow
		want    bool
	}{
		"drawn":            {[]ports.SceneWindow{shown}, true},
		"absent":           {[]ports.SceneWindow{{ID: 8, Rect: shown.Rect}}, false},
		"hidden workspace": {[]ports.SceneWindow{{ID: 7}}, false},
		"hidden":           {[]ports.SceneWindow{{ID: 7, Rect: shown.Rect, Hidden: true}}, false},
		"preview":          {[]ports.SceneWindow{{ID: 7, Rect: shown.Rect, Preview: .5}}, false},
		"zooming":          {[]ports.SceneWindow{{ID: 7, Rect: shown.Rect, Zoom: .9}}, false},
		"faded out":        {[]ports.SceneWindow{{ID: 7, Rect: shown.Rect, Fade: 1}}, false},
	} {
		s := out
		s.Windows = tc.windows
		if got := osdDrawn(s, 7); got != tc.want {
			t.Errorf("%s: osdDrawn = %v, want %v", name, got, tc.want)
		}
	}
}
