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

func TestExpansionPeakLiveUpdate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		k := newMockkms(t)
		events := make(chan ports.OutputEvent, 4)
		o := &Output{Events: events, expansionPeak: make(chan float64, 1)}
		o.conn.name = "DP-1"
		o.mode = modeInfo{VRefresh: 60}
		o.hdr.cap = hdrCapability{Capable: true, HDRMetadata: HDRMetadata{MaxLuminance: 604}}
		o.hdr.on = true
		o.hdr.settings = HDRSettings{Enabled: true, SDRBrightness: 203}
		c := &Card{k: k, outputs: map[string]*Output{"DP-1": o}}
		k.EXPECT().resources().Return([]uint32{30}, []uint32{40}, nil)
		k.EXPECT().connector(uint32(40)).Return(connector{id: 40, name: "DP-1", connected: true, modes: []modeInfo{o.mode}}, nil)
		scan := func(peak float64) {
			t.Helper()
			c.SetWant(Want{HDR: map[string]HDRSettings{"DP-1": {Enabled: true, SDRBrightness: 203, ExpansionPeakNits: peak}}})
			added, removed, replaced, err := c.Scan()
			if err != nil || len(added)+len(removed)+len(replaced) != 0 {
				t.Fatalf("peak-only scan: added=%v removed=%v replaced=%v err=%v", added, removed, replaced, err)
			}
		}
		// A busy owner keeps only the newest request, without blocking Scan.
		scan(700)
		scan(800)
		if len(o.expansionPeak) != 1 {
			t.Fatal("expected one pending peak")
		}
		if o.hdr.settings.ExpansionPeakNits != 0 {
			t.Fatal("scan mutated owner state")
		}
		done := make(chan struct{})
		go func() {
			o.applyExpansionPeak(<-o.expansionPeak)
			close(done)
		}()
		<-done
		got := (<-events).(ports.OutputHDRCaps)
		if got.Output != "DP-1" || !got.Active || got.Caps.PeakNits != 800 || got.Caps.PeakSource != domain.HDRPeakConfig || got.Caps.Headroom != 800.0/203 {
			t.Fatalf("live caps: %+v", got)
		}
		scan(800)
		select {
		case <-o.expansionPeak:
			t.Fatal("unchanged peak woke owner")
		default:
		}
		// Even a duplicate reaching the owner must not emit another snapshot.
		o.applyExpansionPeak(800)
		select {
		case <-events:
			t.Fatal("unchanged peak emitted caps")
		default:
		}
		// Removing the override is also a live update, restoring EDID caps.
		scan(0)
		o.applyExpansionPeak(<-o.expansionPeak)
		if got = (<-events).(ports.OutputHDRCaps); got.Caps.PeakNits != 604 {
			t.Fatalf("reset peak: %+v", got)
		}
	})
}
