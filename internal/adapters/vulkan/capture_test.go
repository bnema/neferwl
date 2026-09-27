package vulkan

import (
	"image"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

func TestCaptureLastFrameRegion(t *testing.T) {
	r, err := New(8, 8)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	if err := render(r, ports.Scene{Background: "#123456"}, nil); err != nil {
		t.Fatal(err)
	}
	dst := make([]byte, 24)
	if err := r.Capture(image.Rect(2, 3, 4, 5), dst, 12); err != nil {
		t.Fatal(err)
	}
	for _, at := range []int{0, 4, 12, 16} {
		if got := dst[at : at+4]; got[0] != 0x56 || got[1] != 0x34 || got[2] != 0x12 || got[3] != 255 {
			t.Fatalf("pixel %d: %x", at, got)
		}
	}
	if err := r.Capture(image.Rect(-1, 0, 1, 1), dst, 12); err == nil {
		t.Fatal("invalid region accepted")
	}
}
