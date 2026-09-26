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
		img, err := loadCursor(tc.change, 2, 256)
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
	if img, err := loadCursor(ports.CursorChange{Shape: "pointer"}, 2, 256); err != nil || img.Pixels[0] != 2 {
		t.Fatalf("cached: %v", err)
	}
	if img, err := loadCursor(ports.CursorChange{Hidden: true}, 1, 256); err != nil || img.W != 0 {
		t.Fatalf("hidden: %+v %v", img, err)
	}
}

// A client cursor drawn at buffer scale 2 is shown at the output scale.
func TestLoadCursorClientImage(t *testing.T) {
	src := &ports.CursorImage{W: 4, H: 2, HotX: 2, HotY: 1, Pixels: make([]byte, 4*2*4)}
	img, err := loadCursor(ports.CursorChange{Image: src, Scale: 2}, 1, 256)
	if err != nil || img.W != 2 || img.H != 1 || img.HotX != 1 || img.HotY != 0 {
		t.Fatalf("%+v %v", img, err)
	}
	img, err = loadCursor(ports.CursorChange{Image: src, Scale: 2}, 2, 256)
	if err != nil || img.W != 4 || img.HotX != 2 {
		t.Fatalf("%+v %v", img, err)
	}
	// Bounded by the cursor plane.
	img, err = loadCursor(ports.CursorChange{Image: src, Scale: 1}, 4, 8)
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
	img, err := loadCursor(ports.CursorChange{Shape: "text"}, 1, 256)
	if err != nil || img.Pixels[0] != 3 {
		t.Fatalf("got arrow instead of the inherited text cursor: %v", err)
	}
}
