package hints

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/neferwl/internal/ports"
)

type harness struct {
	requests chan ports.HintScanRequest
	captures chan ports.CaptureRequest
	captured chan ports.CaptureDone
	results  chan ports.HintScanResult
}

func start(t *testing.T, timeout time.Duration) *harness {
	return startWith(t, timeout, Options{})
}

// startCapturing starts a worker that always captures: debug dumps are on.
func startCapturing(t *testing.T, timeout time.Duration) *harness {
	return startWith(t, timeout, Options{DumpDir: filepath.Join(t.TempDir(), "hints")})
}

func startWith(t *testing.T, timeout time.Duration, opts Options) *harness {
	t.Helper()
	if opts.detector == nil {
		// Tests of the capture flow do not care what the detector finds.
		det := newMockdetector(t)
		det.EXPECT().Detect(mock.Anything, mock.Anything).Return(nil, nil).Maybe()
		det.EXPECT().Close().Return(nil).Maybe()
		opts.detector = det
	}
	h := &harness{
		requests: make(chan ports.HintScanRequest, 1),
		captures: make(chan ports.CaptureRequest, 4),
		captured: make(chan ports.CaptureDone, 1),
		results:  make(chan ports.HintScanResult, 4),
	}
	ctx, cancel := context.WithCancel(context.Background())
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		_ = run(ctx, h.requests, h.captures, h.captured, h.results, timeout, opts)
	}()
	t.Cleanup(func() { cancel(); <-exited })
	return h
}

func TestHintScan(t *testing.T) {
	det := newMockdetector(t)
	det.EXPECT().Close().Return(nil).Once()
	var got detFrame
	det.EXPECT().Detect(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, f detFrame) ([]detection, error) {
		got = f
		return []detection{{x1: 10, y1: 20, x2: 110, y2: 60, score: 0.8}}, nil
	}).Once()
	h := startWith(t, 5*time.Second, Options{detector: det})
	go func() {
		creq := <-h.captures
		h.captured <- ports.CaptureDone{ID: creq.ID, Output: creq.Output}
	}()
	h.requests <- ports.HintScanRequest{Seq: 7, Output: "HEADLESS-1", Width: 64, Height: 32, Scale: 1}
	res := <-h.results
	require.NoError(t, res.Err)
	require.Equal(t, uint64(7), res.Seq)
	require.Equal(t, "HEADLESS-1", res.Output)
	require.Equal(t, []ports.HintTarget{{Rect: ports.Rect{X: 10, Y: 20, W: 100, H: 40}, Score: 0.8}}, res.Targets)
	require.Equal(t, 64, got.width)
	require.Equal(t, 32, got.height)
	require.Equal(t, 64*4, got.stride)
}

func TestTargetsOf(t *testing.T) {
	got := targetsOf([]detection{
		{x1: 11, y1: 21, x2: 100.2, y2: 61, score: 0.5}, // scale 2: x 5..51, y 10..31
		{x1: 0, y1: 0, x2: 5, y2: 40, score: 0.9},       // 2.5 -> 3 logical wide: dropped
		{x1: 0, y1: 0, x2: 40, y2: 5, score: 0.9},       // 3 logical high: dropped
		{x1: 0, y1: 0, x2: 8, y2: 8, score: 0.4},        // exactly 4x4: kept
		{x1: float32(math.NaN()), y1: 0, x2: 50, y2: 50},
	}, 2)
	require.Equal(t, []ports.HintTarget{
		{Rect: ports.Rect{X: 5, Y: 10, W: 46, H: 21}, Score: 0.5},
		{Rect: ports.Rect{X: 0, Y: 0, W: 4, H: 4}, Score: 0.4},
	}, got)
	// A zero scale reads as 1.
	require.Len(t, targetsOf([]detection{{x2: 10, y2: 10}}, 0), 1)
}

// A detector error fails the scan; the worker goes on and the buffer is released.
func TestHintDetectorErrorFailsScan(t *testing.T) {
	boom := errors.New("detector gone")
	det := newMockdetector(t)
	det.EXPECT().Close().Return(nil).Once()
	det.EXPECT().Detect(mock.Anything, mock.Anything).Return(nil, boom).Once()
	det.EXPECT().Detect(mock.Anything, mock.Anything).Return(nil, nil).Once()
	h := startWith(t, 5*time.Second, Options{detector: det})
	for i, want := range []error{boom, nil} {
		go func() {
			creq := <-h.captures
			h.captured <- ports.CaptureDone{ID: creq.ID, Output: creq.Output}
		}()
		h.requests <- ports.HintScanRequest{Seq: uint64(i + 1), Output: "o", Width: 16, Height: 16, Scale: 1}
		res := <-h.results
		if want != nil {
			require.ErrorIs(t, res.Err, want)
		} else {
			require.NoError(t, res.Err)
		}
	}
}

func TestHintRequestShape(t *testing.T) {
	h := startCapturing(t, 5*time.Second)
	got := make(chan ports.CaptureRequest, 1)
	go func() {
		creq := <-h.captures
		got <- creq
		h.captured <- ports.CaptureDone{ID: creq.ID + 1}                  // another ID: ignored
		h.captured <- ports.CaptureDone{ID: creq.ID, Output: creq.Output} // blank frame
	}()
	h.requests <- ports.HintScanRequest{Seq: 1, Output: "o", Width: 64, Height: 32, Scale: 1}
	res := <-h.results
	require.NoError(t, res.Err)
	creq := <-got
	require.NotZero(t, creq.ID&ports.CaptureIDInternal)
	require.False(t, creq.Indicate)
	require.Equal(t, image.Rect(0, 0, 64, 32), creq.Region)
	require.Len(t, creq.Mapped, 64*32*4)
	require.Equal(t, 64*4, creq.Stride)
	require.Equal(t, uint32(1), creq.Format)
}

func TestHintCaptureError(t *testing.T) {
	h := startCapturing(t, 5*time.Second)
	boom := errors.New("boom")
	go func() {
		creq := <-h.captures
		h.captured <- ports.CaptureDone{ID: creq.ID, Err: boom}
	}()
	h.requests <- ports.HintScanRequest{Seq: 3, Output: "o", Width: 64, Height: 32, Scale: 1}
	res := <-h.results
	require.ErrorIs(t, res.Err, boom)
	require.Equal(t, uint64(3), res.Seq)
	require.Empty(t, res.Targets)
}

func TestHintTimeout(t *testing.T) {
	h := startCapturing(t, 50*time.Millisecond)
	h.requests <- ports.HintScanRequest{Seq: 4, Output: "o", Width: 64, Height: 32, Scale: 1}
	res := <-h.results
	require.ErrorIs(t, res.Err, ErrTimeout)
	require.Equal(t, uint64(4), res.Seq)
}

func TestHintBusy(t *testing.T) {
	h := startCapturing(t, 5*time.Second)
	h.requests <- ports.HintScanRequest{Seq: 1, Output: "o", Width: 64, Height: 32, Scale: 1}
	creq := <-h.captures
	h.requests <- ports.HintScanRequest{Seq: 2, Output: "o", Width: 64, Height: 32, Scale: 1}
	res := <-h.results
	require.ErrorIs(t, res.Err, ErrBusy)
	require.Equal(t, uint64(2), res.Seq)
	h.captured <- ports.CaptureDone{ID: creq.ID}
	res = <-h.results
	require.NoError(t, res.Err)
	require.Equal(t, uint64(1), res.Seq)
}

func TestHintBadSize(t *testing.T) {
	h := start(t, time.Second)
	h.requests <- ports.HintScanRequest{Seq: 5, Output: "o"}
	res := <-h.results
	require.ErrorIs(t, res.Err, ErrBadRequest)
}

// waitFiles waits for the dump the worker writes after sending the result.
func waitFiles(t *testing.T, dir string, n int) []os.DirEntry {
	t.Helper()
	var entries []os.DirEntry
	require.Eventually(t, func() bool {
		entries, _ = os.ReadDir(dir)
		return len(entries) >= n
	}, 5*time.Second, 10*time.Millisecond)
	return entries
}

// scanOnce runs one scan on a 64x32 frame and waits for its result.
func scanOnce(t *testing.T, h *harness, seq uint64) ports.HintScanResult {
	t.Helper()
	go func() {
		creq := <-h.captures
		for i := 0; i < len(creq.Mapped); i += 4 {
			creq.Mapped[i], creq.Mapped[i+1], creq.Mapped[i+2] = 10, 20, 30
		}
		h.captured <- ports.CaptureDone{ID: creq.ID, Output: creq.Output}
	}()
	h.requests <- ports.HintScanRequest{Seq: seq, Output: "HEADLESS-1", Width: 64, Height: 32, Scale: 1}
	res := <-h.results
	require.NoError(t, res.Err)
	return res
}

func TestHintDump(t *testing.T) {
	det := newMockdetector(t)
	det.EXPECT().Close().Return(nil).Once()
	det.EXPECT().Detect(mock.Anything, mock.Anything).Return([]detection{{x1: 8, y1: 4, x2: 40, y2: 20, score: 0.75}}, nil).Once()
	dir := filepath.Join(t.TempDir(), "hints")
	h := startWith(t, 5*time.Second, Options{DumpDir: dir, detector: det})
	res := scanOnce(t, h, 1)
	require.Len(t, res.Targets, 1)

	entries := waitFiles(t, dir, 3)
	var png, ann, js string
	for _, e := range entries {
		info, err := e.Info()
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		switch {
		case strings.HasSuffix(e.Name(), ".annotated.png"):
			ann = e.Name()
		case strings.HasSuffix(e.Name(), ".png"):
			png = e.Name()
		case strings.HasSuffix(e.Name(), ".json"):
			js = e.Name()
		}
	}
	require.NotEmpty(t, png)
	require.NotEmpty(t, ann)
	require.Regexp(t, `^\d{8}-\d{6}\.\d{3}-HEADLESS-1\.json$`, js)

	st, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), st.Mode().Perm())

	data, err := os.ReadFile(filepath.Join(dir, js))
	require.NoError(t, err)
	var meta struct {
		Output   string  `json:"output"`
		Width    int     `json:"width"`
		Height   int     `json:"height"`
		Stride   int     `json:"stride"`
		Scale    float64 `json:"scale"`
		DetectMS float64 `json:"detect_ms"`
		TotalMS  float64 `json:"total_ms"`
		Targets  []struct {
			Rect  [4]int  `json:"rect"`
			Score float32 `json:"score"`
		} `json:"targets"`
	}
	require.NoError(t, json.Unmarshal(data, &meta))
	require.Equal(t, "HEADLESS-1", meta.Output)
	require.Equal(t, 64, meta.Width)
	require.Equal(t, 32, meta.Height)
	require.Equal(t, 64*4, meta.Stride)
	require.Equal(t, 1.0, meta.Scale)
	require.GreaterOrEqual(t, meta.TotalMS, meta.DetectMS)
	require.Len(t, meta.Targets, 1)
	require.Equal(t, [4]int{8, 4, 40, 20}, meta.Targets[0].Rect)
	require.Equal(t, float32(0.75), meta.Targets[0].Score)

	f, err := os.Open(filepath.Join(dir, png))
	require.NoError(t, err)
	defer f.Close()
	img, _, err := image.Decode(f)
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 64, 32), img.Bounds())
	r, g, b, _ := img.At(0, 0).RGBA()
	require.Equal(t, uint32(30)*0x101, r)
	require.Equal(t, uint32(20)*0x101, g)
	require.Equal(t, uint32(10)*0x101, b)
}

func TestHintDumpDisabled(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hints")
	h := start(t, 5*time.Second)
	scanOnce(t, h, 1)
	_, err := os.Stat(dir)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestHintDumpRetention(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 60; i++ {
		for _, ext := range []string{".png", ".annotated.png", ".json"} {
			name := fmt.Sprintf("20260101-000000.%03d-o%s", i, ext)
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o600))
		}
	}
	require.NoError(t, prune(dir, maxDumps))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, maxDumps*3)
	require.Equal(t, "20260101-000000.010-o.annotated.png", entries[0].Name())
}

// The worker keeps no frame: the mapped buffer belongs to the scan, and to the
// dump job until it is written; every mapping is released afterwards.
func TestWorkerHoldsNoFrameAfterScan(t *testing.T) {
	for _, dumpDir := range []string{"", filepath.Join(t.TempDir(), "hints")} {
		det := newMockdetector(t)
		det.EXPECT().Detect(mock.Anything, mock.Anything).Return(nil, nil).Once()
		w := &worker{dumpDir: dumpDir, det: det}
		captures := make(chan ports.CaptureRequest, 1)
		captured := make(chan ports.CaptureDone, 1)
		go func() {
			creq := <-captures
			captured <- ports.CaptureDone{ID: creq.ID, Output: creq.Output}
		}()
		req := ports.HintScanRequest{Output: "o", Width: 64, Height: 32, Scale: 1}
		res := w.serve(context.Background(), req, make(chan ports.HintScanRequest), captures, captured, make(chan ports.HintScanResult, 1), 5*time.Second)
		require.NoError(t, res.Err)
		if dumpDir == "" {
			require.Nil(t, w.pending)
			require.Zero(t, w.live, "the frame is released before the result")
		} else {
			require.NotNil(t, w.pending)
			require.Equal(t, 1, w.live, "only the frame, kept for the dump")
		}
		w.dump(context.Background())
		require.Nil(t, w.pending)
		require.Zero(t, w.live)
	}
}

// A capture that timed out stays mapped until the output answers it.
func TestTimedOutCaptureIsReleasedWhenDone(t *testing.T) {
	w := &worker{dumpDir: t.TempDir()}
	captures := make(chan ports.CaptureRequest, 1)
	captured := make(chan ports.CaptureDone, 1)
	req := ports.HintScanRequest{Output: "o", Width: 64, Height: 32, Scale: 1}
	res := w.serve(context.Background(), req, make(chan ports.HintScanRequest), captures, captured, make(chan ports.HintScanResult, 1), 20*time.Millisecond)
	require.ErrorIs(t, res.Err, ErrTimeout)
	creq := <-captures
	require.Equal(t, 1, w.live)
	creq.Mapped[0] = 1 // still valid: the output may write until CaptureDone
	w.reap(creq.ID)
	require.Zero(t, w.live)
}

// A capture that completes after its scan timed out is released while the
// worker is idle, without another scan, and its reply does not linger.
func TestHintLateCaptureReaped(t *testing.T) {
	h := startCapturing(t, 50*time.Millisecond)
	h.requests <- ports.HintScanRequest{Seq: 1, Output: "o", Width: 64, Height: 32, Scale: 1}
	creq := <-h.captures
	res := <-h.results
	require.ErrorIs(t, res.Err, ErrTimeout)
	// The worker is idle; the output finally answers.
	h.captured <- ports.CaptureDone{ID: creq.ID, Output: creq.Output}
	require.Eventually(t, func() bool { return len(h.captured) == 0 }, time.Second, time.Millisecond)
}
