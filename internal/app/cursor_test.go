package app

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// themeCursor writes a one-image Xcursor file of side size under
// dir/theme/cursors/name, every pixel set to px.
func themeCursor(t *testing.T, dir, name string, size int, px byte) {
	t.Helper()
	le := binary.LittleEndian
	out := le.AppendUint32([]byte("Xcur"), 16)
	out = le.AppendUint32(out, 0x10000)
	out = le.AppendUint32(out, 1)
	for _, v := range []int{0xfffd0002, size, 28, 36, 0xfffd0002, size, 1, size, size, 0, 0, 0} {
		out = le.AppendUint32(out, uint32(v))
	}
	for range size * size * 4 {
		out = append(out, px)
	}
	path := filepath.Join(dir, "theme", "cursors", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// resetThemeCursors empties the theme cache now and after the test.
func resetThemeCursors(t *testing.T) {
	reset := func() {
		themeCursors.Lock()
		clear(themeCursors.m)
		themeCursors.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func TestLoadCursorShapes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XCURSOR_PATH", dir)
	t.Setenv("XCURSOR_THEME", "theme")
	t.Setenv("XCURSOR_SIZE", "24")
	resetThemeCursors(t)
	themeCursor(t, dir, "left_ptr", 24, 1)
	themeCursor(t, dir, "hand2", 24, 2)
	for _, tc := range []struct {
		change ports.CursorChange
		px     byte
	}{
		{ports.CursorChange{}, 1},
		// Themes with only X11 names still get the hand.
		{ports.CursorChange{Shape: "pointer"}, 2},
		// Unknown shapes fall back to the arrow.
		{ports.CursorChange{Shape: "zoom-in"}, 1},
	} {
		img, err := loadCursor(tc.change, 2, 0, 256)
		if err != nil {
			t.Fatal(tc.change, err)
		}
		if img.W != 48 || img.Pixels[0] != tc.px {
			t.Errorf("%+v: %dx%d px %d, want 48 wide px %d", tc.change, img.W, img.H, img.Pixels[0], tc.px)
		}
	}
	// Cached: a removed theme file still loads.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if img, err := loadCursor(ports.CursorChange{Shape: "pointer"}, 2, 0, 256); err != nil || img.Pixels[0] != 2 {
		t.Fatalf("cached: %v", err)
	}
	if img, err := loadCursor(ports.CursorChange{Hidden: true}, 1, 0, 256); err != nil || img.W != 0 {
		t.Fatalf("hidden: %+v %v", img, err)
	}
}

// A client cursor drawn at buffer scale 2 is shown at the output scale.
func TestLoadCursorClientImage(t *testing.T) {
	src := &ports.CursorImage{W: 4, H: 2, HotX: 2, HotY: 1, Pixels: make([]byte, 4*2*4)}
	img, err := loadCursor(ports.CursorChange{Image: src, Scale: 2}, 1, 0, 256)
	if err != nil || img.W != 2 || img.H != 1 || img.HotX != 1 || img.HotY != 0 {
		t.Fatalf("%+v %v", img, err)
	}
	img, err = loadCursor(ports.CursorChange{Image: src, Scale: 2}, 2, 0, 256)
	if err != nil || img.W != 4 || img.HotX != 2 {
		t.Fatalf("%+v %v", img, err)
	}
	// Bounded by the cursor plane.
	img, err = loadCursor(ports.CursorChange{Image: src, Scale: 1}, 4, 0, 8)
	if err != nil || img.W != 8 || img.H != 4 {
		t.Fatalf("%+v %v", img, err)
	}
}

// A theme that only overrides the arrow still gets its parent's shapes.
func TestLoadCursorInheritedShape(t *testing.T) {
	dir := t.TempDir()
	resetThemeCursors(t)
	t.Setenv("XCURSOR_PATH", dir)
	t.Setenv("XCURSOR_THEME", "child")
	t.Setenv("XCURSOR_SIZE", "24")
	themeCursor(t, dir, "left_ptr", 24, 1) // in "theme", the parent
	if err := os.MkdirAll(filepath.Join(dir, "child", "cursors"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "child", "index.theme"), []byte("[Icon Theme]\nInherits=theme\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(dir, "child", "cursors", "default")
	themeCursor(t, dir, "text", 24, 3)
	if err := os.Rename(filepath.Join(dir, "theme", "cursors", "left_ptr"), child); err != nil {
		t.Fatal(err)
	}
	img, err := loadCursor(ports.CursorChange{Shape: "text"}, 1, 0, 256)
	if err != nil || img.Pixels[0] != 3 {
		t.Fatalf("got arrow instead of the inherited text cursor: %v", err)
	}
}

// A rotated output holds the cursor image like any buffer: pixels and
// hotspot follow BufferTransform.ToBuffer.
func TestRotateCursor(t *testing.T) {
	// 3×2, pixel i is {i, 0, 0, 255}.
	px := make([]byte, 3*2*4)
	for i := range 6 {
		copy(px[i*4:], []byte{byte(i), 0, 0, 255})
	}
	img := ports.CursorImage{W: 3, H: 2, Pixels: px}
	if got := rotateCursor(img, 0); &got.Pixels[0] != &img.Pixels[0] || got.W != 3 || got.H != 2 {
		t.Fatalf("identity: %+v", got)
	}
	got := rotateCursor(img, 1)
	if got.W != 2 || got.H != 3 || got.HotX != 0 || got.HotY != 2 {
		t.Fatalf("90: %dx%d hot (%d,%d)", got.W, got.H, got.HotX, got.HotY)
	}
	if &img.Pixels[0] == &got.Pixels[0] || img.Pixels[0] != 0 || img.W != 3 {
		t.Fatal("source modified")
	}
	for tr := ports.BufferTransform(1); tr < 8; tr++ {
		img.HotX, img.HotY = 2, 1
		got := rotateCursor(img, tr)
		for y := range 2 {
			for x := range 3 {
				bx, by := tr.ToBuffer(float64(x)+.5, float64(y)+.5, 3, 2)
				if v := got.Pixels[(int(by)*got.W+int(bx))*4]; v != byte(y*3+x) {
					t.Fatalf("transform %d: (%d,%d) landed with value %d", tr, x, y, v)
				}
			}
		}
		// The hotspot pixel keeps its value.
		if v := got.Pixels[(got.HotY*got.W+got.HotX)*4]; v != 5 {
			t.Fatalf("transform %d: hotspot (%d,%d) on pixel %d, want 5", tr, got.HotX, got.HotY, v)
		}
	}
	// Pixels shorter than W*H*4 are returned as they are.
	short := ports.CursorImage{W: 3, H: 2, Pixels: px[:8]}
	if got := rotateCursor(short, 1); got.W != 3 || got.H != 2 || len(got.Pixels) != 8 {
		t.Fatalf("short pixels: %+v", got)
	}
	// 90 degrees: the top left pixel goes to the bottom left.
	if got := rotateCursor(ports.CursorImage{W: 3, H: 2, Pixels: px}, 1); got.Pixels[(2*2+0)*4] != 0 {
		t.Fatalf("90: top-left pixel at %v", got.Pixels)
	}
}
