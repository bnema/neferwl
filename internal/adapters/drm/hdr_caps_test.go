package drm

import (
	"context"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"testing"
	"testing/synctest"
)

func TestHDRCapabilityCaps(t *testing.T) {
	c := hdrCapability{Capable: true, HDRMetadata: HDRMetadata{MaxLuminance: 604, MaxFrameAverage: 277}}
	if got := c.caps(0, 203); got.Tier != domain.HDRFull || got.PeakNits != 604 {
		t.Fatalf("caps: %+v", got)
	}
	if got := c.caps(800, 200); got.PeakSource != domain.HDRPeakConfig || got.Headroom != 4 {
		t.Fatalf("override: %+v", got)
	}
	c.Capable, c.Reason = false, "edid: no PQ"
	if got := c.caps(800, 200); got.Tier != domain.HDRUnavailable || got.Reason != c.Reason {
		t.Fatalf("unavailable: %+v", got)
	}
}

func TestSendHDRCaps(t *testing.T) {
	events := make(chan ports.OutputEvent, 1)
	o := &Output{Events: events}
	o.conn.name = "DP-1"
	o.hdr.cap = hdrCapability{Capable: true, HDRMetadata: HDRMetadata{MaxLuminance: 604}}
	o.hdr.settings.SDRBrightness = 203
	o.hdr.on = true
	o.sendHDRCaps()
	got := (<-events).(ports.OutputHDRCaps)
	if got.Output != "DP-1" || !got.Active || got.Caps.PeakNits != 604 || got.Caps.Tier != domain.HDRFull || got.Caps.Headroom != 604.0/203 {
		t.Fatalf("event: %+v", got)
	}
	o.hdr.settings.ExpansionPeakNits = 800
	o.sendHDRCaps()
	if got = (<-events).(ports.OutputHDRCaps); got.Caps.PeakNits != 800 || got.Caps.PeakSource != domain.HDRPeakConfig {
		t.Fatalf("configured peak: %+v", got)
	}
	o.off = true
	o.sendHDRCaps()
	if got = (<-events).(ports.OutputHDRCaps); got.Active {
		t.Fatal("inactive CRTC reports active HDR")
	}
	o.hdr.cap = hdrCapability{Reason: "edid: no PQ"}
	o.hdr.on = false
	o.sendHDRCaps()
	if got = (<-events).(ports.OutputHDRCaps); got.Active || got.Caps.Tier != domain.HDRUnavailable {
		t.Fatalf("SDR: %+v", got)
	}
	// A full stream is cancellation-aware, never silently dropped.
	o.sendHDRCaps()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o.eventContext = ctx
	o.sendHDRCaps()
	o.Events = nil
	o.sendHDRCaps()
}

func TestHDRCapsBackpressurePreservesSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		events := make(chan ports.OutputEvent, 1)
		events <- ports.OutputAdded{}
		o := &Output{Events: events, eventContext: context.Background()}
		o.conn.name = "DP-1"
		o.hdr.cap = hdrCapability{Capable: true, HDRMetadata: HDRMetadata{MaxLuminance: 604}}
		done := make(chan struct{})
		go func() { o.sendHDRCaps(); close(done) }()
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("full lifecycle stream dropped snapshot")
		default:
		}
		<-events
		<-done
		got := (<-events).(ports.OutputHDRCaps)
		if got.Output != "DP-1" || got.Caps.PeakNits != 604 {
			t.Fatalf("snapshot %+v", got)
		}
	})
}
