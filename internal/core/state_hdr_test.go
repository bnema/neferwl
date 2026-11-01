package core

import (
	"context"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"math"
	"testing"
	"testing/synctest"
)

func TestStateHDRProjection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _, _ := fadeCore(t)
		c.hdrCaps = map[string]domain.OutputHDRCaps{"A": {Output: "A", Active: true, Caps: domain.ResolveHDRCaps(domain.HDRCapsInput{Capable: true, MaxLuminance: 604})}}
		c.mapWindow(ports.WindowMapped{ID: 1, Fullscreen: true, ContentType: domain.ContentGame})
		st := c.state()
		h := st.Outputs[0].HDR
		if h.Tier != "full" || h.PeakNits != 604 || h.PeakSource != "edid" || !h.Active || !h.Expandable || h.SDRExpand != "auto" || h.Headroom != 604.0/203 {
			t.Fatalf("HDR %+v", h)
		}
		e := st.Windows[0].SDRExpansion
		if !e.Active || e.Reason != "eligible" || e.Decode != "gamma22" || math.Abs(e.Intensity-0.5) > 1e-9 || math.Abs(e.Top-(1+0.5*(604.0/203-1))) > 1e-9 {
			t.Fatalf("expansion %+v", e)
		}

		for _, plain := range []bool{false, true} {
			c.clientEvent(context.Background(), ports.WindowColorDescription{ID: 1, PlainSDR: plain})
			next := c.state()
			e := next.Windows[0].SDRExpansion
			if e.Active != plain {
				t.Fatalf("PlainSDR=%t expansion %+v", plain, e)
			}
			if !plain && e.Reason != "colour-managed surface" {
				t.Fatalf("reason %q", e.Reason)
			}
			if sameState(st, next) {
				t.Fatal("colour fact change not detected")
			}
			st = next
		}
		c.hdrCaps["A"] = domain.OutputHDRCaps{Output: "A"}
		next := c.state()
		if sameState(st, next) {
			t.Fatal("HDR change not detected")
		}
		e = next.Windows[0].SDRExpansion
		if e.Active || e.Reason != "output not HDR" || e.Top != 0 || e.Decode != "" {
			t.Fatalf("off %+v", e)
		}
	})
}

func TestStateHDRInitiallyColourManaged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _, _ := fadeCore(t)
		c.hdrCaps = map[string]domain.OutputHDRCaps{"A": {Output: "A", Active: true, Caps: domain.ResolveHDRCaps(domain.HDRCapsInput{Capable: true, MaxLuminance: 604})}}
		c.mapWindow(ports.WindowMapped{ID: 1, Fullscreen: true, ContentType: domain.ContentGame, ColorDescription: true})
		e := c.state().Windows[0].SDRExpansion
		if e.Active || e.Reason != "colour-managed surface" {
			t.Fatalf("initial expansion %+v", e)
		}
	})
}
