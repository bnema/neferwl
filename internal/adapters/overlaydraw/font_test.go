package overlaydraw

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/font/gofont/goregular"
)

// labelOf rasterizes text with a fresh cache-owning Raster and Titles.
func labelOf(t *testing.T, r *Raster, tt *Titles, text string) []byte {
	t.Helper()
	pix, w, h := r.Label(text, 0, 1)
	if pix == nil || w <= 0 || h <= 0 {
		t.Fatal("no label")
	}
	title, _, _ := tt.Title(1, text, Style{FontPx: 13, MaxWidth: 400}, 0)
	if title == nil {
		t.Fatal("no title")
	}
	return append(append([]byte(nil), pix...), title...)
}

// A font file replaces Go Mono in the caches that already drew with it; a
// bad path falls back to Go Mono with an error; an empty one restores it.
func TestLoadFont(t *testing.T) {
	t.Cleanup(func() { _ = LoadFont("") })
	var r Raster
	var tt Titles
	mono := labelOf(t, &r, &tt, "Wide iiii")

	path := filepath.Join(t.TempDir(), "goregular.ttf")
	if err := os.WriteFile(path, goregular.TTF, 0o600); err != nil {
		t.Fatal(err)
	}
	gen := FontGen()
	if err := LoadFont(path); err != nil {
		t.Fatal(err)
	}
	if FontGen() == gen {
		t.Fatal("generation kept")
	}
	if regular := labelOf(t, &r, &tt, "Wide iiii"); bytes.Equal(regular, mono) {
		t.Fatal("cached rasters kept Go Mono")
	}

	for _, bad := range []string{filepath.Join(t.TempDir(), "missing.ttf"), writeFile(t, []byte("not a font"))} {
		if err := LoadFont(bad); err == nil {
			t.Fatalf("%s loaded", bad)
		}
		if got := labelOf(t, &r, &tt, "Wide iiii"); !bytes.Equal(got, mono) {
			t.Fatalf("%s: not Go Mono", bad)
		}
	}
	if err := LoadFont(path); err != nil {
		t.Fatal(err)
	}
	if err := LoadFont(""); err != nil {
		t.Fatal(err)
	}
	if got := labelOf(t, &r, &tt, "Wide iiii"); !bytes.Equal(got, mono) {
		t.Fatal("empty path: not Go Mono")
	}
}

func writeFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bad.ttf")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
