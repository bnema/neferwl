package vulkan

import (
	"testing"
	"unsafe"
)

// Cursor images are packed (pitch = size*4), exported, and WriteCursor
// crops to the image and clears around the pixels.
func TestCursorBuffersWriteCursor(t *testing.T) {
	r, err := New(64, 48)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	bufs, err := r.CursorBuffers(64)
	if err != nil {
		t.Skipf("no exportable cursor images: %v", err)
	}
	for _, b := range bufs {
		if p := b.Planes[0]; p.Stride != 64*4 || p.Offset != 0 || b.Format != fourccARGB {
			t.Fatalf("layout %+v format %#x", p, b.Format)
		}
		b.Planes[0].File.Close()
	}
	mem := func() []byte { c := r.cursors[1]; return unsafe.Slice((*byte)(c.mapped), c.pitch*c.size) }
	// A first image fills the corner; a second, smaller one must clear it.
	big := make([]byte, 100*100*4)
	for i := range big {
		big[i] = 0xff
	}
	if err := r.WriteCursor(1, big, 100, 100); err != nil { // cropped to 64
		t.Fatal(err)
	}
	if m := mem(); m[63*256+63*4] != 0xff {
		t.Fatal("cropped image not written")
	}
	if err := r.WriteCursor(1, []byte{1, 2, 3, 4}, 1, 1); err != nil {
		t.Fatal(err)
	}
	m := mem()
	if m[0] != 1 || m[3] != 4 || m[4] != 0 || m[256] != 0 || m[63*256+63*4] != 0 {
		t.Fatal("image not cleared around the new pixels")
	}
	if err := r.WriteCursor(1, []byte{1}, 1, 1); err == nil {
		t.Fatal("short image accepted")
	}
}
