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

	portsmocks "github.com/bnema/nefertty/internal/mocks/ports"
	"github.com/bnema/nefertty/internal/ports"
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
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, c map[ports.WindowID]ports.SurfaceContent) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.scenes = append(f.scenes, s)
		f.contents = append(f.contents, maps.Clone(c))
		return err
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
	contents <- ports.SurfaceContent{ID: 1, Pixels: []byte{1}}
	scenes <- ports.Scene{Seq: 1}
	scenes <- ports.Scene{Seq: 2}
	contents <- ports.SurfaceContent{ID: 2, Pixels: []byte{2}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Width: 2, Height: 2, ScreenshotDir: dir, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, contents)
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

func TestErrors(t *testing.T) {
	expected := errors.New("failure")
	scenes := make(chan ports.Scene, 1)
	scenes <- ports.Scene{}
	if err := Run(context.Background(), Options{NewRenderer: func(int, int) (ports.Renderer, error) { return nil, expected }}, scenes, nil); !errors.Is(err, expected) {
		t.Fatal(err)
	}
	r, _ := recordingRenderer(t, expected)
	if err := Run(context.Background(), Options{NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, nil); !errors.Is(err, expected) {
		t.Fatal(err)
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
	contents <- ports.SurfaceContent{ID: 42, Width: 1, Height: 1, Stride: 4, Pixels: []byte{0, 0, 255, 255}}
	r, f := recordingRenderer(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Width: 2, Height: 2, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, contents)
	}()
	deadline := time.After(3 * time.Second)
	for {
		s, c := f.snapshot()
		if len(c) > 0 && len(s[0].Layers) == 1 && c[0][42].Pixels != nil {
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
