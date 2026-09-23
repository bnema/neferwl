package vulkan

import (
	"image/color"
	"testing"
)

func TestRendererClear(t *testing.T) {
	r, err := New(64, 48)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	if err := r.Clear([3]uint8{0x10, 0x20, 0x30}); err != nil {
		t.Fatal(err)
	}
	pixels := r.Pixels()
	want := color.RGBA{0x10, 0x20, 0x30, 255}
	for _, p := range [][2]int{{0, 0}, {63, 47}} {
		if got := pixels.At(p[0], p[1]); got != want {
			t.Errorf("pixel %v = %v, want %v", p, got, want)
		}
	}
}
