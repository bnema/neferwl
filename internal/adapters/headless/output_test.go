package headless

import (
	"context"
	"errors"
	"image"
	"image/png"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/logging"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

// frames records what the mocked renderer is asked to draw.
type frames struct {
	mu       sync.Mutex
	scenes   []ports.Scene
	contents []map[ports.WindowID]ports.SurfaceContent
}

func (f *frames) snapshot() ([]ports.Scene, []map[ports.WindowID]ports.SurfaceContent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ports.Scene(nil), f.scenes...), append([]map[ports.WindowID]ports.SurfaceContent(nil), f.contents...)
}

// recordingRenderer returns a generated mock whose Render records each frame
// and returns err. Close must be called exactly once.
func recordingRenderer(t *testing.T, err error) (*portsmocks.MockRenderer, *frames) {
	r := portsmocks.NewMockRenderer(t)
	f := &frames{}
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, c map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.scenes = append(f.scenes, s)
		f.contents = append(f.contents, maps.Clone(c))
		return nil, err
	}).Maybe()
	r.EXPECT().Pixels().Return(image.NewRGBA(image.Rect(0, 0, 2, 2))).Maybe()
	r.EXPECT().Close().Return().Once()
	return r, f
}

// waitFrames polls until the renderer drew at least n frames.
func waitFrames(t *testing.T, f *frames, n int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if s, _ := f.snapshot(); len(s) >= n {
			return
		}
		select {
		case <-deadline:
			t.Fatal("no frame")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	scenes := make(chan ports.Scene, 8)
	contents := make(chan ports.SurfaceContent, 8)
	r, f := recordingRenderer(t, nil)
	contents <- ports.SurfaceContent{ID: 1, SHM: &ports.SHMBuffer{Pool: 1}}
	shown := []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 2, H: 2}}, {ID: 2, Rect: ports.Rect{W: 2, H: 2}}}
	scenes <- ports.Scene{Seq: 1, OutputWidth: 2, OutputHeight: 2, Windows: shown}
	scenes <- ports.Scene{Seq: 2, OutputWidth: 2, OutputHeight: 2, Windows: shown}
	contents <- ports.SurfaceContent{ID: 2, SHM: &ports.SHMBuffer{Pool: 2}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Width: 2, Height: 2, ScreenshotDir: dir, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, contents, nil, nil)
	}()
	waitFrames(t, f, 1)
	if s, c := f.snapshot(); len(s) != 1 || s[0].Seq != 2 || len(c[0]) != 2 {
		t.Errorf("coalescing: %+v %+v", s, c)
	}
	contents <- ports.SurfaceContent{ID: 1}
	waitFrames(t, f, 2)
	if _, c := f.snapshot(); len(c) < 2 {
		t.Fatal("no second frame")
	} else if _, ok := c[1][1]; ok {
		t.Error("content not deleted")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"frame-000001.png", "frame-000002.png", "latest.png"} {
		file, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		_, err = png.Decode(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}

// Content of a window the output does not show draws no frame.
func TestRunSkipsContentNotShown(t *testing.T) {
	scenes := make(chan ports.Scene, 1)
	contents := make(chan ports.SurfaceContent, 2)
	scenes <- ports.Scene{Seq: 1, OutputWidth: 2, OutputHeight: 2, Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 2, H: 2}}, {ID: 3, Hidden: true}}}
	r, f := recordingRenderer(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Width: 2, Height: 2, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, contents, nil, nil)
	}()
	waitFrames(t, f, 1)
	contents <- ports.SurfaceContent{ID: 2, SHM: &ports.SHMBuffer{Pool: 2}}
	contents <- ports.SurfaceContent{ID: 3, SHM: &ports.SHMBuffer{Pool: 3}}
	time.Sleep(50 * time.Millisecond)
	if s, _ := f.snapshot(); len(s) != 1 {
		t.Fatalf("%d frames for content not shown", len(s))
	}
	contents <- ports.SurfaceContent{ID: 1, SHM: &ports.SHMBuffer{Pool: 1}}
	waitFrames(t, f, 2)
	if _, c := f.snapshot(); c[1][2].SHM == nil {
		t.Error("content not shown was dropped")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestErrors(t *testing.T) {
	expected := errors.New("failure")
	scenes := make(chan ports.Scene, 1)
	scenes <- ports.Scene{}
	if err := Run(context.Background(), Options{NewRenderer: func(int, int) (ports.Renderer, error) { return nil, expected }}, scenes, nil, nil, nil); !errors.Is(err, expected) {
		t.Fatal(err)
	}
	r, _ := recordingRenderer(t, expected)
	if err := Run(context.Background(), Options{NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, nil, nil, nil); !errors.Is(err, expected) {
		t.Fatal(err)
	}
}

// Each trim tick lets the idle renderer free what it no longer draws,
// without a frame; a failed trim stops the output.
func TestRunTrimsRendererOnTick(t *testing.T) {
	ticks := make(chan time.Time)
	clk := portsmocks.NewMockClock(t)
	tk := portsmocks.NewMockTicker(t)
	clk.EXPECT().NewTicker(trimEvery).Return(tk).Once()
	tk.EXPECT().C().Return(ticks)
	tk.EXPECT().Stop().Return().Once()
	now := time.Unix(1000, 0)
	clk.EXPECT().Now().Return(now)
	r, f := recordingRenderer(t, nil)
	trimmed := make(chan time.Time, 2)
	failure := errors.New("trim failed")
	calls := 0 // Run's goroutine only
	r.EXPECT().Trim(now).RunAndReturn(func(at time.Time) error {
		trimmed <- at
		if calls++; calls == 2 {
			return failure
		}
		return nil
	}).Twice()
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Options{Width: 2, Height: 2, Clock: clk, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, nil, nil, nil, nil)
	}()
	ticks <- now
	if got := <-trimmed; !got.Equal(now) {
		t.Fatalf("trimmed at %v", got)
	}
	ticks <- now
	if err := <-done; !errors.Is(err, failure) {
		t.Fatalf("run: %v", err)
	}
	if s, _ := f.snapshot(); len(s) != 0 {
		t.Fatalf("%d frames for a trim", len(s))
	}
}

func TestWritePNGAtomic(t *testing.T) {
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	for _, name := range []string{"frame-000001.png", "latest.png"} {
		path := filepath.Join(dir, name)
		if err := writePNG(path, img); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := png.Decode(f); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("unexpected files: %v", entries)
	}
}

func TestRunPassesLayerContent(t *testing.T) {
	scenes := make(chan ports.Scene, 1)
	contents := make(chan ports.SurfaceContent, 1)
	layer := ports.SceneLayer{ID: 42, Layer: ports.LayerTop, Rect: ports.Rect{W: 2, H: 2}}
	scenes <- ports.Scene{Layers: []ports.SceneLayer{layer}}
	contents <- ports.SurfaceContent{ID: 42, Width: 1, Height: 1, SHM: &ports.SHMBuffer{Pool: 42, Stride: 4}}
	r, f := recordingRenderer(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Width: 2, Height: 2, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, contents, nil, nil)
	}()
	deadline := time.After(3 * time.Second)
	for {
		s, c := f.snapshot()
		if len(c) > 0 && len(s[0].Layers) == 1 && c[0][42].SHM != nil {
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("layer content not passed to renderer")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCaptureForcesFreshHeadlessFrame(t *testing.T) {
	scenes := make(chan ports.Scene, 1)
	requests := make(chan ports.CaptureRequest, 1)
	replies := make(chan ports.CaptureDone, 1)
	r, frames := recordingRenderer(t, nil)

	r.EXPECT().Capture(image.Rect(0, 0, 2, 2), mock.MatchedBy(func(p []byte) bool { return len(p) >= 16 }), 8).RunAndReturn(func(_ image.Rectangle, dst []byte, _ int) error { copy(dst, []byte{1, 2, 3, 255}); return nil }).Once()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Width: 2, Height: 2, Captured: replies, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, nil, nil, requests)
	}()
	scenes <- ports.Scene{Background: "#000000"}
	waitFrames(t, frames, 1)
	f, err := os.CreateTemp(t.TempDir(), "shot")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(16); err != nil {
		t.Fatal(err)
	}
	requests <- ports.CaptureRequest{ID: 1, Region: image.Rect(0, 0, 2, 2), Width: 2, Height: 2, Stride: 8, Dst: ports.SHMBuffer{File: f}}
	select {
	case result := <-replies:
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no capture")
	}
	r.Calls = nil // Testify must not inspect unmapped slice during expectation cleanup.
	if _, err := f.Stat(); err == nil {
		t.Fatal("descriptor not closed")
	}
	if s, _ := frames.snapshot(); len(s) < 2 {
		t.Fatal("no fresh frame")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Only the output which successfully prepared HDR reports confirmed HDR.
// The fallback must explicitly withdraw it, and close exported descriptors.
func TestHeadlessHDRFormatsConfirmed(t *testing.T) {
	for _, success := range []bool{true, false} {
		t.Run(map[bool]string{true: "confirmed", false: "fallback"}[success], func(t *testing.T) {
			r := portsmocks.NewMockRenderer(t)
			r.EXPECT().SetHDR(float64(203)).Return().Once()
			if success {
				f, err := os.CreateTemp(t.TempDir(), "target")
				if err != nil {
					t.Fatal(err)
				}
				r.EXPECT().ExportTargets(1, []uint64(nil)).Return([]ports.DMABuf{{Planes: []ports.DMABufPlane{{File: f}}}}, nil).Once()
				t.Cleanup(func() {
					if _, err := f.Stat(); err == nil {
						t.Error("exported fd not closed")
					}
				})
			} else {
				r.EXPECT().ExportTargets(1, []uint64(nil)).Return(nil, errors.New("no compatible target")).Once()
				r.EXPECT().SetHDR(float64(0)).Return().Once()
				r.EXPECT().ExportTargets(0, []uint64(nil)).Return(nil, nil).Once()
			}
			r.EXPECT().Close().Return().Once()
			formats := make(chan ports.OutputFormats, 1)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				done <- Run(ctx, Options{Width: 2, Height: 2, HDR: true, Name: "HEADLESS-1", Formats: formats, Log: logging.For(ctx, "render"), NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, nil, nil, nil, nil)
			}()
			select {
			case f := <-formats:
				if f.Output != "HEADLESS-1" || (f.HDR != nil) != success {
					t.Fatalf("report %+v, success=%t", f, success)
				}
				if success && (f.HDR.MaxLuminance != 1000 || f.HDR.MaxFrameAverage != 400 || f.HDR.MinLuminance != .005) {
					t.Fatalf("HDR metadata %+v", f.HDR)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("no format report")
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
