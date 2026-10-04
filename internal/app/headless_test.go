package app

import (
	"testing"

	"github.com/bnema/neferwl/internal/adapters/vulkan"
	"github.com/bnema/neferwl/internal/ports"
)

// The headless renderer factory tells a real renderer it drives a virtual
// output: without it ExportTargets(1, nil) refuses HDR targets on a real GPU
// and a headless output falls back to SDR. Skips without Vulkan or without a
// device that exports dmabufs (lavapipe in CI).
func TestVirtualRendererExportsHDRTargets(t *testing.T) {
	newRenderer := func(w, h int) (ports.Renderer, error) {
		r, err := vulkan.New(w, h)
		if err != nil {
			return nil, err
		}
		return r, nil
	}
	r, err := virtualRenderer(newRenderer, false)(64, 16)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	// A device that cannot export at all says nothing about the wiring.
	sdr, err := r.ExportTargets(1, nil)
	if err != nil {
		t.Skipf("no exportable target: %v", err)
	}
	for _, b := range sdr {
		for _, p := range b.Planes {
			_ = p.File.Close()
		}
	}
	r.SetHDR(203)
	bufs, err := r.ExportTargets(1, nil)
	if err != nil {
		t.Fatalf("virtual HDR export: %v", err)
	}
	for _, b := range bufs {
		for _, p := range b.Planes {
			_ = p.File.Close()
		}
	}
}
