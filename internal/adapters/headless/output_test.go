package headless

import (
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/ports"
)

type fakeRenderer struct {
	mu       sync.Mutex
	frames   []ports.Scene
	contents []map[ports.WindowID]ports.SurfaceContent
	closed   bool
	err      error
}

func (r *fakeRenderer) Render(s ports.Scene, c map[ports.WindowID]ports.SurfaceContent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, s)
	copyMap := make(map[ports.WindowID]ports.SurfaceContent)
	for id, v := range c {
		copyMap[id] = v
	}
	r.contents = append(r.contents, copyMap)
	return r.err
}
func (r *fakeRenderer) Pixels() *image.RGBA { return image.NewRGBA(image.Rect(0, 0, 2, 2)) }
func (r *fakeRenderer) Close()              { r.mu.Lock(); defer r.mu.Unlock(); r.closed = true }
func TestRun(t *testing.T) {
	dir := t.TempDir()
	scenes := make(chan ports.Scene, 8)
	contents := make(chan ports.SurfaceContent, 8)
	r := new(fakeRenderer)
	contents <- ports.SurfaceContent{ID: 1, Pixels: []byte{1}}
	scenes <- ports.Scene{Seq: 1}
	scenes <- ports.Scene{Seq: 2}
	contents <- ports.SurfaceContent{ID: 2, Pixels: []byte{2}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Width: 2, Height: 2, ScreenshotDir: dir, NewRenderer: func(int, int) (Renderer, error) { return r, nil }}, scenes, contents)
	}()
	wait := func(n int) {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for {
			r.mu.Lock()
			count := len(r.frames)
			r.mu.Unlock()
			if count >= n {
				return
			}
			select {
			case <-deadline:
				t.Fatal("no frame")
			case <-time.After(time.Millisecond * 10):
			}
		}
	}
	wait(1)
	r.mu.Lock()
	if len(r.frames) != 1 || r.frames[0].Seq != 2 || len(r.contents[0]) != 2 {
		t.Errorf("coalescing: %+v %+v", r.frames, r.contents)
	}
	r.mu.Unlock()
	contents <- ports.SurfaceContent{ID: 1}
	wait(2)
	r.mu.Lock()
	if _, ok := r.contents[1][1]; ok {
		t.Error("content not deleted")
	}
	r.mu.Unlock()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	if !r.closed {
		t.Error("renderer not closed")
	}
	r.mu.Unlock()
	for _, name := range []string{"frame-000001.png", "frame-000002.png", "latest.png"} {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		_, err = png.Decode(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}
func TestErrors(t *testing.T) {
	expected := errors.New("failure")
	scenes := make(chan ports.Scene, 1)
	scenes <- ports.Scene{}
	if err := Run(context.Background(), Options{NewRenderer: func(int, int) (Renderer, error) { return nil, expected }}, scenes, nil); !errors.Is(err, expected) {
		t.Fatal(err)
	}
	r := &fakeRenderer{err: expected}
	if err := Run(context.Background(), Options{NewRenderer: func(int, int) (Renderer, error) { return r, nil }}, scenes, nil); !errors.Is(err, expected) {
		t.Fatal(err)
	}
	if !r.closed {
		t.Fatal("not closed")
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
