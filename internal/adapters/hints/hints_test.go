package hints

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/png"
	"os"
	"os/exec"
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

func loadBGRA(t *testing.T) (pix []byte, w, hgt int) {
	t.Helper()
	f, err := os.Open("testdata/browser.png")
	require.NoError(t, err)
	defer f.Close()
	img, _, err := image.Decode(f)
	require.NoError(t, err)
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	for i := 0; i < len(rgba.Pix); i += 4 {
		rgba.Pix[i], rgba.Pix[i+2] = rgba.Pix[i+2], rgba.Pix[i]
		rgba.Pix[i+3] = 0xff
	}
	return rgba.Pix, b.Dx(), b.Dy()
}

func TestHintScan(t *testing.T) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("tesseract not on PATH")
	}
	pix, w, hgt := loadBGRA(t)
	h := start(t, 5*time.Second)

	// Plays the output owner.
	go func() {
		creq := <-h.captures
		copy(creq.Mapped, pix)
		h.captured <- ports.CaptureDone{ID: creq.ID, Output: creq.Output}
	}()

	h.requests <- ports.HintScanRequest{Seq: 7, Output: "HEADLESS-1", Width: w, Height: hgt, Scale: 1, Windows: []ports.HintWindow{{ID: 1, Rect: ports.Rect{W: w, H: hgt}}}}
	res := <-h.results
	require.NoError(t, res.Err)
	require.Equal(t, uint64(7), res.Seq)
	require.Equal(t, "HEADLESS-1", res.Output)
	var links int
	for _, tg := range res.Targets {
		require.Positive(t, tg.Rect.W)
		require.Positive(t, tg.Rect.H)
		if tg.Kind == ports.HintLink {
			links++
		}
	}
	require.GreaterOrEqual(t, links, 1)
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

func scanOnce(t *testing.T, h *harness, pix []byte, w, hgt int, seq uint64) ports.HintScanResult {
	t.Helper()
	go func() {
		creq := <-h.captures
		copy(creq.Mapped, pix)
		h.captured <- ports.CaptureDone{ID: creq.ID, Output: creq.Output}
	}()
	h.requests <- ports.HintScanRequest{Seq: seq, Output: "HEADLESS-1", Width: w, Height: hgt, Scale: 1, Windows: []ports.HintWindow{{ID: 1, Rect: ports.Rect{W: w, H: hgt}}}}
	res := <-h.results
	require.NoError(t, res.Err)
	return res
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

func TestHintDump(t *testing.T) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("tesseract not on PATH")
	}
	pix, w, hgt := loadBGRA(t)
	dir := filepath.Join(t.TempDir(), "hints")
	h := startWith(t, 5*time.Second, Options{DumpDir: dir})
	res := scanOnce(t, h, pix, w, hgt, 1)
	require.NotEmpty(t, res.Targets)

	entries := waitFiles(t, dir, 3)
	// A second request is only served once the dump is written.
	_ = scanOnceDone(t, h)
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
		Output    string  `json:"output"`
		Width     int     `json:"width"`
		Height    int     `json:"height"`
		Stride    int     `json:"stride"`
		Scale     float64 `json:"scale"`
		LinksOnly bool    `json:"links_only"`
		Targets   []struct {
			Kind string `json:"kind"`
			Rect [4]int `json:"rect"`
		} `json:"targets"`
	}
	require.NoError(t, json.Unmarshal(data, &meta))
	require.Equal(t, "HEADLESS-1", meta.Output)
	require.Equal(t, w, meta.Width)
	require.Equal(t, hgt, meta.Height)
	require.Equal(t, w*4, meta.Stride)
	require.Equal(t, 1.0, meta.Scale)
	require.Len(t, meta.Targets, len(res.Targets))
	for i, tg := range res.Targets {
		require.Equal(t, [4]int{tg.Rect.X, tg.Rect.Y, tg.Rect.X + tg.Rect.W, tg.Rect.Y + tg.Rect.H}, meta.Targets[i].Rect)
	}

	f, err := os.Open(filepath.Join(dir, png))
	require.NoError(t, err)
	defer f.Close()
	img, _, err := image.Decode(f)
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, w, hgt), img.Bounds())
	r, g, b, _ := img.At(0, 0).RGBA()
	require.Equal(t, uint32(pix[2])*0x101, r)
	require.Equal(t, uint32(pix[1])*0x101, g)
	require.Equal(t, uint32(pix[0])*0x101, b)
}

// scanOnceDone sends a request that fails fast on a blank capture and waits
// for its result, which proves the previous dump finished.
func scanOnceDone(t *testing.T, h *harness) ports.HintScanResult {
	t.Helper()
	go func() {
		creq := <-h.captures
		h.captured <- ports.CaptureDone{ID: creq.ID, Err: errors.New("stop")}
	}()
	h.requests <- ports.HintScanRequest{Seq: 99, Output: "x", Width: 8, Height: 8, Scale: 1, Windows: []ports.HintWindow{{ID: 1, Rect: ports.Rect{W: 8, H: 8}}}}
	return <-h.results
}

func TestHintDumpDisabled(t *testing.T) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("tesseract not on PATH")
	}
	pix, w, hgt := loadBGRA(t)
	dir := filepath.Join(t.TempDir(), "hints")
	h := start(t, 5*time.Second)
	scanOnce(t, h, pix, w, hgt, 1)
	scanOnceDone(t, h)
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

func TestScanMergesSource(t *testing.T) {
	pix, w, hgt := loadBGRA(t)
	src := newMocktargetSource(t)
	src.EXPECT().Close().Return(nil).Once() // the worker closes its source on return
	wins := []ports.HintWindow{{ID: 4, PID: 9, Rect: ports.Rect{X: 0, Y: 0, W: w, H: hgt}}}
	at := []ports.HintTarget{{Rect: ports.Rect{X: 5, Y: 5, W: 30, H: 10}, Kind: ports.HintLink}}
	src.EXPECT().Targets(mock.Anything, wins, false).Return(at, map[ports.WindowID]bool{4: true}, nil).Once()
	h := startWith(t, 5*time.Second, Options{Source: src})
	go func() {
		creq := <-h.captures
		copy(creq.Mapped, pix)
		h.captured <- ports.CaptureDone{ID: creq.ID, Output: creq.Output}
	}()
	h.requests <- ports.HintScanRequest{Seq: 1, Output: "HEADLESS-1", Width: w, Height: hgt, Scale: 1, Windows: wins}
	res := <-h.results
	require.NoError(t, res.Err)
	// The whole frame is covered: only the accessibility target remains.
	require.Equal(t, at, res.Targets)
}

func TestScanSourceErrorUsesOCR(t *testing.T) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("tesseract not on PATH")
	}
	pix, w, hgt := loadBGRA(t)
	src := newMocktargetSource(t)
	src.EXPECT().Close().Return(nil).Once() // the worker closes its source on return
	wins := []ports.HintWindow{{ID: 4, PID: 9, Rect: ports.Rect{W: w, H: hgt}}}
	src.EXPECT().Targets(mock.Anything, wins, false).Return(nil, nil, errors.New("bus gone")).Once()
	h := startWith(t, 5*time.Second, Options{Source: src})
	go func() {
		creq := <-h.captures
		copy(creq.Mapped, pix)
		h.captured <- ports.CaptureDone{ID: creq.ID, Output: creq.Output}
	}()
	h.requests <- ports.HintScanRequest{Seq: 1, Output: "HEADLESS-1", Width: w, Height: hgt, Scale: 1, Windows: wins}
	res := <-h.results
	require.NoError(t, res.Err)
	require.NotEmpty(t, res.Targets)
}

// The worker keeps no frame: the mapped buffer belongs to the scan, and to the
// dump job until it is written; every mapping is released afterwards.
func TestWorkerHoldsNoFrameAfterScan(t *testing.T) {
	for _, dumpDir := range []string{"", filepath.Join(t.TempDir(), "hints")} {
		// OCR is "installed" but its binary cannot run: only the memory matters.
		w := &worker{dumpDir: dumpDir, ocr: ocrState{done: true, binary: "/nonexistent/tesseract", lang: "eng"}}
		requests := make(chan ports.HintScanRequest)
		captures := make(chan ports.CaptureRequest, 1)
		captured := make(chan ports.CaptureDone, 1)
		results := make(chan ports.HintScanResult, 1)
		go func() {
			creq := <-captures
			captured <- ports.CaptureDone{ID: creq.ID, Output: creq.Output}
		}()
		req := ports.HintScanRequest{Output: "o", Width: 64, Height: 32, Scale: 1, Windows: []ports.HintWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 32}}}}
		res := w.serve(context.Background(), req, requests, captures, captured, results, 5*time.Second)
		require.NoError(t, res.Err)
		if dumpDir == "" {
			require.Nil(t, w.pending)
			require.Zero(t, w.live, "frame and crops are released before the result")
		} else {
			require.NotNil(t, w.pending)
			require.Equal(t, 1, w.live, "only the frame, kept for the dump")
		}
		w.dump(context.Background())
		require.Nil(t, w.pending)
		require.Zero(t, w.live)
		require.Empty(t, w.cache) // OCR failed: nothing cached
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

// Without tesseract OCR is off for good: no capture, only accessibility targets.
func TestNoTesseractSkipsCapture(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	button := ports.HintTarget{Kind: ports.HintButton, Rect: ports.Rect{W: 5, H: 5}}
	covered := ports.HintTarget{Kind: ports.HintLink, Rect: ports.Rect{X: 30, W: 5, H: 5}}
	wins := []ports.HintWindow{{ID: 1, Rect: ports.Rect{W: 20, H: 20}}, {ID: 2, Rect: ports.Rect{X: 20, W: 20, H: 20}}}
	src := newMocktargetSource(t)
	src.EXPECT().Close().Return(nil).Once() // the worker closes its source on return
	src.EXPECT().Targets(mock.Anything, wins, false).Return([]ports.HintTarget{button, covered}, map[ports.WindowID]bool{2: true}, nil).Times(2)
	h := startWith(t, time.Second, Options{Source: src})
	for seq := uint64(1); seq <= 2; seq++ {
		h.requests <- ports.HintScanRequest{Seq: seq, Output: "o", Width: 40, Height: 20, Scale: 1, Windows: wins}
		res := <-h.results
		require.NoError(t, res.Err)
		require.Equal(t, []ports.HintTarget{button, covered}, res.Targets)
	}
	require.Empty(t, h.captures)
}

// With debug dumps on, the frame is still captured (for the dump only).
func TestNoTesseractDumpStillCaptures(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := filepath.Join(t.TempDir(), "hints")
	h := startWith(t, 5*time.Second, Options{DumpDir: dir})
	go func() {
		creq := <-h.captures
		h.captured <- ports.CaptureDone{ID: creq.ID, Output: creq.Output}
	}()
	h.requests <- ports.HintScanRequest{Seq: 1, Output: "o", Width: 40, Height: 20, Scale: 1, Windows: []ports.HintWindow{{ID: 1, Rect: ports.Rect{W: 40, H: 20}}}}
	require.NoError(t, (<-h.results).Err)
	waitFiles(t, dir, 3)
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
