package vulkan

import (
	"encoding/binary"
	"image"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSwizzleXR30ToXB30(t *testing.T) {
	for _, tc := range []struct {
		name    string
		r, g, b uint32
	}{
		{"zero", 0, 0, 0}, {"max", 1023, 1023, 1023}, {"red", 1023, 0, 0}, {"green", 0, 1023, 0},
		{"blue", 0, 0, 1023}, {"mixed", 0x2a5, 0x15a, 0x3c3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			xr30 := 3<<30 | tc.r<<20 | tc.g<<10 | tc.b // padding set: must be cleared
			want := PackXB30(uint16(tc.r), uint16(tc.g), uint16(tc.b))
			if got := SwizzleXR30ToXB30(xr30); got != want {
				t.Fatalf("got %#08x want %#08x", got, want)
			}
			if want&0x3ff != tc.r || want>>10&0x3ff != tc.g || want>>20&0x3ff != tc.b || want>>30 != 0 {
				t.Fatalf("PackXB30 layout %#08x", want)
			}
		})
	}
}

// The PQ capture of an HDR frame equals the display target's words with R
// and B swapped: same shader, no tone mapping.
func TestBeginCapturePQMatchesDisplayTarget(t *testing.T) {
	r := hdrTestRenderer(t)
	scene, contents, _ := hdrPQScene(t, r)
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	display := pqTargetWords(t, r)
	f, err := r.BeginCapturePQ()
	if err != nil {
		t.Fatal(err)
	}
	defer r.EndCapture(f)
	if done := f.Done(); done != nil {
		if err := pollSyncFile(done, make([]unix.PollFd, 1)); err != nil {
			t.Fatal(err)
		}
	}
	dst := make([]byte, r.width*r.height*4)
	if err := f.Read(image.Rect(0, 0, r.width, r.height), dst, r.width*4); err != nil {
		t.Fatal(err)
	}
	for i, w := range display {
		got := binary.LittleEndian.Uint32(dst[i*4:]) &^ (3 << 30)
		if want := SwizzleXR30ToXB30(w); got != want {
			t.Fatalf("pixel %d: got %#08x want %#08x (display %#08x)", i, got, want, w)
		}
	}
}

func TestBeginCapturePQNeedsHDRFrame(t *testing.T) {
	r := newTestRenderer(t, 8, 8)
	t.Cleanup(r.Close)
	if _, err := r.BeginCapturePQ(); err == nil {
		t.Fatal("PQ capture without an HDR frame")
	}
}
