package xcursor

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// file builds an Xcursor file with one square image per size; the hotspot is
// size/8 and every pixel is opaque white.
func file(sizes ...int) []byte {
	le := binary.LittleEndian
	out := le.AppendUint32([]byte("Xcur"), 16)
	out = le.AppendUint32(out, 0x10000)
	out = le.AppendUint32(out, uint32(len(sizes)))
	pos := 16 + 12*len(sizes)
	for _, s := range sizes {
		out = le.AppendUint32(out, chunkImage)
		out = le.AppendUint32(out, uint32(s))
		out = le.AppendUint32(out, uint32(pos))
		pos += 36 + s*s*4
	}
	for _, s := range sizes {
		for _, v := range []int{36, chunkImage, s, 1, s, s, s / 8, s / 8, 0} {
			out = le.AppendUint32(out, uint32(v))
		}
		for range s * s {
			out = append(out, 255, 255, 255, 255)
		}
	}
	return out
}

func TestDecodePicksSize(t *testing.T) {
	data := file(24, 36, 48)
	for _, tc := range []struct{ want, got int }{{24, 24}, {30, 36}, {36, 36}, {40, 48}, {96, 48}, {1, 24}} {
		img, err := Decode(data, tc.want)
		if err != nil || img.W != tc.got || img.HotX != tc.got/8 || len(img.Pixels) != tc.got*tc.got*4 {
			t.Errorf("size %d: %+v %v", tc.want, img.W, err)
		}
	}
}

func TestDecodeRejectsBadFiles(t *testing.T) {
	good := file(24)
	for name, data := range map[string][]byte{
		"empty":     nil,
		"magic":     append([]byte("Xcux"), good[4:]...),
		"toc":       good[:20],
		"pixels":    good[:len(good)-4],
		"no images": file(),
	} {
		if _, err := Decode(data, 24); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// A huge declared size must not allocate.
	huge := file(24)
	binary.LittleEndian.PutUint32(huge[16+12+16:], 1<<20)
	if _, err := Decode(huge, 24); err == nil {
		t.Error("huge image accepted")
	}
}

func TestScale(t *testing.T) {
	img, _ := Decode(file(48), 48)
	s := img.Scale(36, 36)
	if s.W != 36 || s.H != 36 || s.HotX != 4 || len(s.Pixels) != 36*36*4 || s.Pixels[0] != 255 {
		t.Fatalf("%+v", s.W)
	}
}

func TestLoadFollowsInherits(t *testing.T) {
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "child"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "child", "index.theme"), []byte("[Icon Theme]\nInherits=parent\n"), 0o644))
	must(os.MkdirAll(filepath.Join(dir, "parent", "cursors"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "parent", "cursors", "left_ptr"), file(24), 0o644))
	img, err := Load([]string{dir}, "child", []string{"default", "left_ptr"}, 24)
	if err != nil || img.W != 24 {
		t.Fatal(img.W, err)
	}
	if _, err := Load([]string{dir}, "child", []string{"nope"}, 24); err == nil {
		t.Fatal("missing cursor found")
	}
}

func TestSystemTheme(t *testing.T) {
	img, err := Load(SearchPath(), os.Getenv("XCURSOR_THEME"), []string{"default", "left_ptr"}, 24)
	if err != nil {
		t.Skip("no cursor theme installed")
	}
	if img.W < 16 || img.W > 64 {
		t.Fatal(img.W)
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(file(24, 48), 24)
	f.Add(file(32), 64)
	f.Fuzz(func(t *testing.T, data []byte, size int) {
		img, err := Decode(data, size)
		if err == nil && (len(img.Pixels) != img.W*img.H*4 || img.HotX >= img.W || img.HotY >= img.H) {
			t.Fatalf("%dx%d hot %d,%d len %d", img.W, img.H, img.HotX, img.HotY, len(img.Pixels))
		}
	})
}

func TestLoadInheritsCycle(t *testing.T) {
	dir := t.TempDir()
	for theme, parent := range map[string]string{"a": "b", "b": "a"} {
		if err := os.MkdirAll(filepath.Join(dir, theme), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, theme, "index.theme"), []byte("[Icon Theme]\nInherits="+parent+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Load([]string{dir}, "a", []string{"default"}, 24); err == nil {
		t.Fatal("found a cursor in an empty cycle")
	}
}
