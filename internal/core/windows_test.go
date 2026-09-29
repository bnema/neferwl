package core

import (
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

func TestWindowRegistryDropForgetsEverything(t *testing.T) {
	r := newWindowRegistry()
	now := time.Unix(10, 0)
	r.mapped(ports.WindowMapped{ID: 1, AppID: "a", PID: 7}, now)
	r.setAppID(1, "b")
	r.setRegion(ports.InputRegionChanged{ID: 1, Rects: []ports.Rect{{W: 1, H: 1}}})
	r.setInhibitShortcuts(1, true)
	r.setIdleInhibit(1, true)
	rec := r.lookup(1)
	if rec.client.AppID != "b" || rec.client.PID != 7 || !rec.mappedAt.Equal(now) || !rec.inhibitShortcuts || !rec.idleInhibit {
		t.Fatalf("record = %+v", rec)
	}
	if r.acceptsInput(1, 5, 5) {
		t.Fatal("point outside the region accepted")
	}
	r.drop(1)
	if len(r.records) != 0 {
		t.Fatalf("records left after drop: %v", r.records)
	}
	if !r.acceptsInput(1, 5, 5) {
		t.Fatal("dropped window keeps its region")
	}
}

func TestWindowRegistryAppIDNeedsMap(t *testing.T) {
	r := newWindowRegistry()
	r.setIdleInhibit(1, true)
	r.setAppID(1, "a")
	if got := r.lookup(1).client.AppID; got != "" {
		t.Fatalf("unmapped window got app ID %q", got)
	}
}

func TestWindowRegistryLayerGone(t *testing.T) {
	r := newWindowRegistry()
	r.setRegion(ports.InputRegionChanged{ID: 2, Rects: []ports.Rect{{W: 1, H: 1}}})
	r.setRegion(ports.InputRegionChanged{ID: 3, Rects: []ports.Rect{{W: 1, H: 1}}})
	r.setIdleInhibit(3, true)
	r.layerGone(2)
	r.layerGone(3)
	if _, ok := r.records[2]; ok {
		t.Fatal("empty layer record kept")
	}
	if rec := r.records[3]; rec == nil || rec.region != nil || !rec.idleInhibit {
		t.Fatalf("layer 3 record = %+v", rec)
	}
	// Turning off an unknown surface creates nothing.
	r.setInhibitShortcuts(4, false)
	r.setIdleInhibit(4, false)
	if _, ok := r.records[4]; ok {
		t.Fatal("record created on release")
	}
}
