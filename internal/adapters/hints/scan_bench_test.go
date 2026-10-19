package hints

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// BenchmarkScan runs whole scans of a 5120x2160 frame with a noisy window
// (the cache never hits) and reports the Go heap bytes allocated per scan.
func BenchmarkScan(b *testing.B) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		b.Skip("tesseract not on PATH")
	}
	const w, h = 5120, 2160
	ctx := context.Background()
	w0 := &worker{}
	requests := make(chan ports.HintScanRequest)
	captures := make(chan ports.CaptureRequest, 1)
	captured := make(chan ports.CaptureDone, 1)
	results := make(chan ports.HintScanResult, 1)
	req := ports.HintScanRequest{Output: "o", Width: w, Height: h, Scale: 1, Windows: []ports.HintWindow{{ID: 1, Rect: ports.Rect{W: w, H: h}}}}
	if os.Getenv("NEFERWL_HINT_BENCH_PGM") == "0" {
		rawPGM = false
	}
	b.ReportAllocs()
	var peak uint64
	for i := range b.N {
		go func() {
			creq := <-captures
			creq.Mapped[0] = byte(i) // each scan differs: no cache hit
			creq.Mapped[1] = byte(i >> 8)
			captured <- ports.CaptureDone{ID: creq.ID, Output: creq.Output}
		}()
		res := w0.serve(ctx, req, requests, captures, captured, results, 5*time.Second)
		if res.Err != nil {
			b.Fatal(res.Err)
		}
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		peak = max(peak, m.HeapSys-m.HeapReleased)
	}
	b.ReportMetric(float64(peak)/1e6, "heapMB")
}
