package hints

import (
	"context"
	"image"
	"os"
	"os/exec"
	"runtime"
	"testing"
)

// BenchmarkScan recognizes an uncached 5120x2160 crop and reports heap usage.
func BenchmarkScan(b *testing.B) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		b.Skip("tesseract not on PATH")
	}
	const w, h = 5120, 2160
	ctx := context.Background()
	img := image.NewGray(image.Rect(0, 0, w, h))
	if os.Getenv("NEFERWL_HINT_BENCH_PGM") == "0" {
		old := rawPGM
		b.Cleanup(func() { rawPGM = old })
		rawPGM = false
	}
	b.ReportAllocs()
	var peak uint64
	for i := range b.N {
		img.Pix[0], img.Pix[1] = byte(i), byte(i>>8)
		if _, _, err := readWords(ctx, img); err != nil {
			b.Fatal(err)
		}
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		peak = max(peak, m.HeapSys-m.HeapReleased)
	}
	b.ReportMetric(float64(peak)/1e6, "heapMB")
}
