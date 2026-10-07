package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/logging"
)

// The headless renderer factory tells a real renderer it drives a virtual
// output: without it ExportTargets(1, nil) refuses HDR targets on a real GPU
// and a headless output falls back to SDR. Skips without Vulkan or without a
// device that exports dmabufs (lavapipe in CI).
func TestVirtualRendererExportsHDRTargets(t *testing.T) {
	r, err := newVulkanRenderer(logging.For(t.Context(), "render"), true, false)(64, 16)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	// A device that cannot export at all says nothing about the wiring.
	sdr, err := r.ExportTargets(1, nil, false)
	if err != nil {
		t.Skipf("no exportable target: %v", err)
	}
	for _, b := range sdr {
		for _, p := range b.Planes {
			_ = p.File.Close()
		}
	}
	r.SetHDR(203)
	bufs, err := r.ExportTargets(1, nil, false)
	if err != nil {
		t.Fatalf("virtual HDR export: %v", err)
	}
	for _, b := range bufs {
		for _, p := range b.Planes {
			_ = p.File.Close()
		}
	}
}

// A failed assembly returns its error and no session: without a runtime
// directory wayland.New, the last fallible step, fails.
func TestAssembleSessionFailure(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	allowPath := filepath.Join(t.TempDir(), "capture-allow")
	s, err := assembleSession(t.Context(), Options{Backend: "headless", Config: config.Defaults(), NoXwayland: true, captureAllowPath: allowPath, captureAllowOwner: uint32(os.Getuid())})
	if err == nil || !strings.Contains(err.Error(), "XDG_RUNTIME_DIR") {
		t.Fatalf("expected runtime directory failure, got %v", err)
	}
	if s != nil {
		t.Fatal("failed assembly returned a session")
	}
}
