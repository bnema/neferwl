package hints

import (
	"context"
	"image"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestFrameBenchmark logs counts and timings, never filenames or OCR text.
func TestFrameBenchmark(t *testing.T) {
	dir := os.Getenv("NEFERWL_HINT_BENCH_DIR")
	if dir == "" {
		t.Skip("set NEFERWL_HINT_BENCH_DIR to a local PNG directory")
	}
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("tesseract not on PATH")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.png"))
	if err != nil || len(files) == 0 {
		t.Fatal("no PNG frames")
	}
	// Settings under test: NEFERWL_HINT_BENCH_WORKERS=<n>, NEFERWL_HINT_BENCH_PNG=speed|none.
	if v, _ := strconv.Atoi(os.Getenv("NEFERWL_HINT_BENCH_WORKERS")); v > 0 {
		ocrWorkers = v
	}
	if os.Getenv("NEFERWL_HINT_BENCH_PGM") == "0" {
		rawPGM = false
	}
	if os.Getenv("NEFERWL_HINT_BENCH_PNG") == "none" {
		pngLevel = png.NoCompression
	}
	for i, file := range files {
		f, err := os.Open(file)
		if err != nil {
			t.Fatal("open frame failed")
		}
		src, err := png.Decode(f)
		_ = f.Close()
		if err != nil {
			t.Fatal("decode frame failed")
		}
		img := image.NewRGBA(image.Rect(0, 0, src.Bounds().Dx(), src.Bounds().Dy()))
		draw.Draw(img, img.Bounds(), src, src.Bounds().Min, draw.Src)
		start := time.Now()
		words, bands, err := readWords(context.Background(), toGray(img))
		if err != nil {
			t.Fatal("recognition failed")
		}
		t.Logf("frame=%d px=%d ocr_ms=%.1f words=%d links=%d bands=%d", i, img.Bounds().Dx()*img.Bounds().Dy(), ms(time.Since(start)), len(words), len(textLinks(words, 1)), bands)
		if os.Getenv("NEFERWL_HINT_BENCH_STRIPS") == "" {
			continue
		}
		// Experimental strips use known OCR word regions, not pixel targets.
		// This is an oracle comparison, not a production optimization.
		lines := make([]image.Rectangle, 0, len(words))
		for _, word := range words {
			lines = append(lines, word.Rect)
		}
		for _, normalized := range []bool{false, true} {
			begin := time.Now()
			compact, _, err := readStrips(context.Background(), img, lines, 8, normalized)
			if err != nil {
				t.Fatal("strip recognition failed")
			}
			t.Logf("frame=%d strips normalized=%t ocr_ms=%.1f words=%d links=%d", i, normalized, ms(time.Since(begin)), len(compact), len(textLinks(compact, 1)))
		}
	}
}

func toGray(img *image.RGBA) *image.Gray {
	g := image.NewGray(img.Bounds())
	draw.Draw(g, g.Bounds(), img, img.Bounds().Min, draw.Src)
	return g
}
