package headless

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"maps"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/mock"
)

// frames records what the mocked renderer is asked to draw.
type frames struct {
	mu       sync.Mutex
	scenes   []domain.Scene
	contents []map[domain.WindowID]domain.SurfaceContent
}

func (f *frames) snapshot() ([]domain.Scene, []map[domain.WindowID]domain.SurfaceContent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Scene(nil), f.scenes...), append([]map[domain.WindowID]domain.SurfaceContent(nil), f.contents...)
}

// recordingRenderer returns a generated mock whose Render records each frame
// and returns err. Close must be called exactly once.
func recordingRenderer(t *testing.T, err error) (*portsmocks.MockRenderer, *frames) {
	r := portsmocks.NewMockRenderer(t)
	f := &frames{}
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s domain.Scene, c map[domain.WindowID]domain.SurfaceContent) (*os.File, error) {
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

// waitFrames lets the owner finish the input already sent to it.
func waitFrames(t *testing.T, f *frames, n int) {
	t.Helper()
	synctest.Wait()
	if s, _ := f.snapshot(); len(s) < n {
		t.Fatalf("got %d frames, want at least %d", len(s), n)
	}
}

func TestRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		scenes := make(chan domain.Scene, 8)
		contents := newContentFeed(t, 8)
		r, f := recordingRenderer(t, nil)
		contents.send(domain.SurfaceContent{ID: 1, SHM: &domain.SHMBuffer{Pool: 1}})
		shown := []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 2, H: 2}}, {ID: 2, Rect: domain.Rect{W: 2, H: 2}}}
		scenes <- domain.Scene{Seq: 1, OutputWidth: 2, OutputHeight: 2, Windows: shown}
		scenes <- domain.Scene{Seq: 2, OutputWidth: 2, OutputHeight: 2, Windows: shown}
		contents.send(domain.SurfaceContent{ID: 2, SHM: &domain.SHMBuffer{Pool: 2}})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, Options{Width: 2, Height: 2, ScreenshotDir: dir, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, contents.in(), contents.ret, nil, nil)
		}()
		waitFrames(t, f, 1)
		if s, c := f.snapshot(); len(s) != 1 || s[0].Seq != 2 || len(c[0]) != 2 {
			t.Errorf("coalescing: %+v %+v", s, c)
		}
		// An empty content of a window the scene still draws (it unmapped
		// and fades out) keeps the last one; a scene without it drops it.
		contents.send(domain.SurfaceContent{ID: 1})
		waitFrames(t, f, 2)
		if _, c := f.snapshot(); len(c) < 2 {
			t.Fatal("no second frame")
		} else if kept, ok := c[1][1]; !ok || kept.SHM == nil || kept.SHM.Pool != 1 {
			t.Errorf("content of the shown window dropped on its empty content: %+v", c[1])
		}
		scenes <- domain.Scene{Seq: 3, OutputWidth: 2, OutputHeight: 2, Windows: shown[1:]}
		waitFrames(t, f, 3)
		if _, c := f.snapshot(); len(c) < 3 {
			t.Fatal("no third frame")
		} else if _, ok := c[2][1]; ok {
			t.Error("content not deleted")
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"frame-000001.png", "frame-000002.png", "frame-000003.png", "latest.png"} {
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
		index, err := os.ReadFile(filepath.Join(dir, "frames.ffconcat"))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(index)), "\n")
		if len(lines) != 8 || lines[0] != "ffconcat version 1.0" {
			t.Fatalf("unexpected index: %s", index)
		}
		for i, want := range []string{"file 'frame-000001.png'", "file 'frame-000002.png'", "file 'frame-000003.png'"} {
			if lines[1+2*i] != want {
				t.Fatalf("frame %d: %s", i+1, lines[1+2*i])
			}
			if i < 3 {
				line := lines[2+2*i]
				value, err := strconv.ParseFloat(strings.TrimPrefix(line, "duration "), 64)
				if !strings.HasPrefix(line, "duration ") || err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
					t.Fatalf("invalid duration: %q", line)
				}
			}
		}
		if lines[7] != lines[5] {
			t.Fatalf("missing final repeated frame: %s", index)
		}
	})
}

// A scene whose Seq the output already holds is not drawn again.
func TestHeadlessSameSeqRendersOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scenes := make(chan domain.Scene)
		contents := newContentFeed(t, 0)
		r, f := recordingRenderer(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, Options{Width: 2, Height: 2, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, contents.in(), contents.ret, nil, nil)
		}()
		scene := domain.Scene{Seq: 5, OutputWidth: 2, OutputHeight: 2, Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 2, H: 2}}}}
		scenes <- scene
		waitFrames(t, f, 1)
		scenes <- scene
		// Unshown content sends Run round its loop: the held scene is handled.
		contents.send(domain.SurfaceContent{ID: 99, Seq: 1, SHM: &domain.SHMBuffer{}})
		contents.send(domain.SurfaceContent{ID: 99, Seq: 2, SHM: &domain.SHMBuffer{}})
		if s, _ := f.snapshot(); len(s) != 1 {
			t.Fatalf("%d frames for a scene with the held Seq", len(s))
		}
		scene.Seq = 6
		scenes <- scene
		waitFrames(t, f, 2)
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if s, _ := f.snapshot(); len(s) != 2 {
			t.Fatalf("%d frames, want 2", len(s))
		}
	})
}

// Content of a window the output does not show draws no frame.
func TestRunSkipsContentNotShown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scenes := make(chan domain.Scene, 1)
		contents := newContentFeed(t, 2)
		scenes <- domain.Scene{Seq: 1, OutputWidth: 2, OutputHeight: 2, Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 2, H: 2}}, {ID: 3, Hidden: true}}}
		r, f := recordingRenderer(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, Options{Width: 2, Height: 2, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, contents.in(), contents.ret, nil, nil)
		}()
		waitFrames(t, f, 1)
		contents.send(domain.SurfaceContent{ID: 2, SHM: &domain.SHMBuffer{Pool: 2}})
		contents.send(domain.SurfaceContent{ID: 3, SHM: &domain.SHMBuffer{Pool: 3}})
		synctest.Wait()
		if s, _ := f.snapshot(); len(s) != 1 {
			t.Fatalf("%d frames for content not shown", len(s))
		}
		contents.send(domain.SurfaceContent{ID: 1, SHM: &domain.SHMBuffer{Pool: 1}})
		waitFrames(t, f, 2)
		if _, c := f.snapshot(); c[1][2].SHM == nil {
			t.Error("content not shown was dropped")
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestErrors(t *testing.T) {
	expected := errors.New("failure")
	scenes := make(chan domain.Scene, 1)
	scenes <- domain.Scene{}
	if err := Run(context.Background(), Options{NewRenderer: func(int, int) (ports.Renderer, error) { return nil, expected }}, scenes, nil, nil, nil, nil); !errors.Is(err, expected) {
		t.Fatal(err)
	}
	r, _ := recordingRenderer(t, expected)
	if err := Run(context.Background(), Options{NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, nil, nil, nil, nil); !errors.Is(err, expected) {
		t.Fatal(err)
	}
}

// Each trim tick lets the idle renderer free what it no longer draws,
// without a frame; a failed trim stops the output.
func TestRunTrimsRendererOnTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
		var statsLog bytes.Buffer // written by Run's goroutine, read after it ends
		log := zerowrap.New(zerowrap.Config{Level: "info", Format: "json", Output: &statsLog})
		go func() {
			done <- Run(context.Background(), Options{Width: 2, Height: 2, Clock: clk, Log: log, Name: "HEADLESS-1", NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, nil, nil, nil, nil, nil)
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
		// A trim tick that succeeded logs the periodic stats once.
		if got := strings.Count(statsLog.String(), `"message":"stats"`); got != 1 {
			t.Fatalf("%d stats entries: %s", got, statsLog.String())
		}
		for _, want := range []string{`"output":"HEADLESS-1"`, `"frames":0`} {
			if !strings.Contains(statsLog.String(), want) {
				t.Fatalf("stats entry lacks %s: %s", want, statsLog.String())
			}
		}
	})
}

// latest.png is the frame file linked, not encoded again: the same pixels,
// published atomically, and nothing left behind.
func TestLinkPNGSecure(t *testing.T) {
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 7)
	}
	frame, latest := filepath.Join(dir, "frame-000001.png"), filepath.Join(dir, "latest.png")
	if err := writePNGSecure(frame, img, nil, domain.SecurityState{}); err != nil {
		t.Fatal(err)
	}
	// An older latest.png is replaced.
	if err := os.WriteFile(latest, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if written, err := linkPNGSecure(frame, latest, img, nil, domain.SecurityState{}); err != nil || !written {
		t.Fatalf("link = %v, %v", written, err)
	}
	a, err := os.ReadFile(frame)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(latest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("latest.png differs from the frame")
	}
	fi, err := os.Stat(frame)
	if err != nil {
		t.Fatal(err)
	}
	li, err := os.Stat(latest)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(fi, li) {
		t.Fatal("latest.png is a copy, not a hard link to the frame")
	}
	got, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	for y := range 2 {
		for x := range 3 {
			if g, w := color.RGBAModel.Convert(got.At(x, y)), img.RGBAAt(x, y); g != color.Color(w) {
				t.Errorf("(%d,%d) = %v, want %v", x, y, g, w)
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("leftover files: %v", entries)
	}
}

// A frame that crossed a security change is not published as latest.png, a
// failed rename leaves no temporary file, and a source that cannot be linked
// is encoded instead.
func TestLinkPNGSecureSkipsAndFallsBack(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	good := domain.SecurityState{Generation: 1}
	// frameIn writes the frame to link and returns its directory and path.
	frameIn := func(t *testing.T) (string, string) {
		t.Helper()
		dir := t.TempDir()
		frame := filepath.Join(dir, "frame-000001.png")
		if err := writePNGSecure(frame, img, nil, domain.SecurityState{}); err != nil {
			t.Fatal(err)
		}
		return dir, frame
	}
	// onlyFrame fails unless the frame is all the directory holds: no
	// latest.png and no latest.png.tmp.
	onlyFrame := func(t *testing.T, dir string) {
		t.Helper()
		if entries, _ := os.ReadDir(dir); len(entries) != 1 {
			t.Fatalf("latest.png or a temp file delivered: %v", entries)
		}
	}
	t.Run("protected state", func(t *testing.T) {
		dir, frame := frameIn(t)
		// A protected state is refused without reading the live snapshot.
		gate := portsmocks.NewMockSessionSecurity(t)
		if written, err := linkPNGSecure(frame, filepath.Join(dir, "latest.png"), img, gate, domain.SecurityState{Generation: 1, Protected: true}); err != nil || written {
			t.Fatalf("link = %v, %v", written, err)
		}
		onlyFrame(t, dir)
	})
	t.Run("protected snapshot", func(t *testing.T) {
		dir, frame := frameIn(t)
		gate := portsmocks.NewMockSessionSecurity(t)
		gate.EXPECT().Snapshot().Return(domain.SecurityState{Generation: 1, Protected: true})
		if written, err := linkPNGSecure(frame, filepath.Join(dir, "latest.png"), img, gate, good); err != nil || written {
			t.Fatalf("link = %v, %v", written, err)
		}
		onlyFrame(t, dir)
	})
	t.Run("changed after the link", func(t *testing.T) {
		dir, frame := frameIn(t)
		gate := portsmocks.NewMockSessionSecurity(t)
		gate.EXPECT().Snapshot().Return(good).Once()
		gate.EXPECT().Snapshot().Return(domain.SecurityState{Generation: 2}).Once()
		if written, err := linkPNGSecure(frame, filepath.Join(dir, "latest.png"), img, gate, good); err != nil || written {
			t.Fatalf("link = %v, %v", written, err)
		}
		onlyFrame(t, dir)
	})
	t.Run("rename failure", func(t *testing.T) {
		dir, frame := frameIn(t)
		// A non-empty directory cannot be replaced by a file.
		latest := filepath.Join(dir, "latest.png")
		if err := os.MkdirAll(filepath.Join(latest, "keep"), 0o700); err != nil {
			t.Fatal(err)
		}
		if written, err := linkPNGSecure(frame, latest, img, nil, domain.SecurityState{}); err == nil || written {
			t.Fatalf("link = %v, %v; want an error", written, err)
		}
		if _, err := os.Stat(latest + ".tmp"); !os.IsNotExist(err) {
			t.Fatalf("temporary link left behind: %v", err)
		}
		if _, err := os.Stat(filepath.Join(latest, "keep")); err != nil {
			t.Fatalf("existing directory touched: %v", err)
		}
	})
	t.Run("no source file", func(t *testing.T) {
		dir := t.TempDir()
		latest := filepath.Join(dir, "latest.png")
		if written, err := linkPNGSecure(filepath.Join(dir, "missing.png"), latest, img, nil, domain.SecurityState{}); err != nil || !written {
			t.Fatalf("link = %v, %v", written, err)
		}
		fi, err := os.Stat(latest)
		if err != nil {
			t.Fatal(err)
		}
		if !fi.Mode().IsRegular() {
			t.Fatalf("latest.png is %v", fi.Mode())
		}
		f, err := os.Open(latest)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := png.Decode(f); err != nil {
			t.Fatal(err)
		}
		// Encoded on its own: nothing else in the directory shares it.
		if entries, _ := os.ReadDir(dir); len(entries) != 1 {
			t.Fatalf("unexpected files: %v", entries)
		}
		if n, ok := linkCount(fi); ok && n != 1 {
			t.Fatalf("latest.png has %d links, want 1", n)
		}
	})
}

// linkCount returns the hard link count of fi where the platform reports it.
func linkCount(fi os.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Nlink), true
}

func TestWritePNGAtomic(t *testing.T) {
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	for _, name := range []string{"frame-000001.png", "latest.png"} {
		path := filepath.Join(dir, name)
		if err := writePNGSecure(path, img, nil, domain.SecurityState{}); err != nil {
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
	synctest.Test(t, func(t *testing.T) {
		scenes := make(chan domain.Scene, 1)
		contents := newContentFeed(t, 1)
		layer := domain.SceneLayer{ID: 42, Layer: domain.LayerTop, Rect: domain.Rect{W: 2, H: 2}}
		scenes <- domain.Scene{Layers: []domain.SceneLayer{layer}}
		contents.send(domain.SurfaceContent{ID: 42, Width: 1, Height: 1, SHM: &domain.SHMBuffer{Pool: 42, Stride: 4}})
		r, f := recordingRenderer(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, Options{Width: 2, Height: 2, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, contents.in(), contents.ret, nil, nil)
		}()
		waitFrames(t, f, 1)
		if s, c := f.snapshot(); len(s[0].Layers) != 1 || c[0][42].SHM == nil {
			t.Fatal("layer content not passed to renderer")
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestCaptureForcesFreshHeadlessFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scenes := make(chan domain.Scene, 1)
		requests := make(chan domain.CaptureRequest, 1)
		replies := make(chan domain.CaptureDone, 1)
		r, frames := recordingRenderer(t, nil)

		frame := portsmocks.NewMockCaptureFrame(t)
		frame.EXPECT().Done().Return(nil)
		frame.EXPECT().Read(image.Rect(0, 0, 2, 2), mock.MatchedBy(func(p []byte) bool { return len(p) >= 16 }), 8).RunAndReturn(func(_ image.Rectangle, dst []byte, _ int) error { copy(dst, []byte{1, 2, 3, 255}); return nil }).Once()
		r.EXPECT().BeginCapture().Return(frame, nil).Once()
		r.EXPECT().EndCapture(frame).Return().Once()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, Options{Width: 2, Height: 2, Captured: replies, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, nil, nil, nil, requests)
		}()
		scenes <- domain.Scene{Background: "#000000"}
		waitFrames(t, frames, 1)
		f, err := os.CreateTemp(t.TempDir(), "shot")
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(16); err != nil {
			t.Fatal(err)
		}
		requests <- domain.CaptureRequest{ID: 1, Region: image.Rect(0, 0, 2, 2), Width: 2, Height: 2, Stride: 8, Dst: domain.SHMBuffer{File: f}}
		select {
		case result := <-replies:
			if result.Err != nil {
				t.Fatal(result.Err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no capture")
		}
		frame.Calls = nil // Testify must not inspect unmapped slice during expectation cleanup.
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
	})
}

// RawHDR writes the renderer's raw PQ codes as a 16-bit latest-pq.png next
// to the 8-bit screenshots.
func TestHeadlessRawHDRScreenshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		want := image.NewRGBA64(image.Rect(0, 0, 2, 2))
		for i := range 4 {
			want.SetRGBA64(i%2, i/2, color.RGBA64{R: uint16(i*0x1111 + 0x0123), G: 0x8000 + uint16(i), B: 0xffc0, A: 0xffff})
		}
		f, err := os.CreateTemp(t.TempDir(), "target")
		if err != nil {
			t.Fatal(err)
		}
		r := portsmocks.NewMockRenderer(t)
		r.EXPECT().SetHDR(float64(203)).Return().Once()
		r.EXPECT().ExportTargets(1, []uint64(nil), false).Return([]domain.DMABuf{{Planes: []domain.DMABufPlane{{File: f}}}}, nil).Once()
		rendered := make(chan struct{}, 4)
		r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(domain.Scene, map[domain.WindowID]domain.SurfaceContent) (*os.File, error) {
			rendered <- struct{}{}
			return nil, nil
		}).Maybe()
		r.EXPECT().Pixels().Return(image.NewRGBA(image.Rect(0, 0, 2, 2))).Maybe()
		r.EXPECT().HDRPixels().Return(want).Maybe()
		r.EXPECT().Close().Return().Once()
		scenes := make(chan domain.Scene, 1)
		scenes <- domain.Scene{Seq: 1, OutputWidth: 2, OutputHeight: 2}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, Options{Width: 2, Height: 2, HDR: true, RawHDR: true, ScreenshotDir: dir, Log: logging.For(ctx, "render"), NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, nil, nil, nil, nil)
		}()
		select {
		case <-rendered:
		case <-time.After(3 * time.Second):
			t.Fatal("no frame")
		}
		// The files are written after Render returns.
		path := filepath.Join(dir, "latest-pq.png")
		synctest.Wait()
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("no latest-pq.png: %v", err)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		got, err := png.Decode(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got.(*image.NRGBA64); !ok && got.ColorModel() != color.RGBA64Model {
			t.Fatalf("not a 16-bit image: %T", got)
		}
		for y := range 2 {
			for x := range 2 {
				if g, w := color.RGBA64Model.Convert(got.At(x, y)), want.RGBA64At(x, y); g != color.Color(w) {
					t.Errorf("(%d,%d) = %v, want %v", x, y, g, w)
				}
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "latest.png")); err != nil {
			t.Errorf("8-bit screenshot: %v", err)
		}
	})
}

// Only the output which successfully prepared HDR reports confirmed HDR.
// The fallback must explicitly withdraw it, and close exported descriptors.
func TestHeadlessHDRFormatsConfirmed(t *testing.T) {
	for _, success := range []bool{true, false} {
		t.Run(map[bool]string{true: "confirmed", false: "fallback"}[success], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := portsmocks.NewMockRenderer(t)
				r.EXPECT().SetHDR(float64(203)).Return().Once()
				if success {
					f, err := os.CreateTemp(t.TempDir(), "target")
					if err != nil {
						t.Fatal(err)
					}
					r.EXPECT().ExportTargets(1, []uint64(nil), false).Return([]domain.DMABuf{{Planes: []domain.DMABufPlane{{File: f}}}}, nil).Once()
					t.Cleanup(func() {
						if _, err := f.Stat(); err == nil {
							t.Error("exported fd not closed")
						}
					})
				} else {
					r.EXPECT().ExportTargets(1, []uint64(nil), false).Return(nil, errors.New("no compatible target")).Once()
					r.EXPECT().SetHDR(float64(0)).Return().Once()
					r.EXPECT().ExportTargets(0, []uint64(nil), false).Return(nil, nil).Once()
				}
				r.EXPECT().Close().Return().Once()
				formats := make(chan domain.OutputFormats, 1)
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() {
					done <- Run(ctx, Options{Width: 2, Height: 2, HDR: true, Name: "HEADLESS-1", Formats: formats, Log: logging.For(ctx, "render"), NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, nil, nil, nil, nil, nil)
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
		})
	}
}

// One batch with two windows applies both before the frame they show in, and
// goes back to wayland reset and exactly once.
func TestRunAppliesWholeBatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scenes := make(chan domain.Scene, 1)
		contents := newContentFeed(t, 1)
		shown := []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 2, H: 2}}, {ID: 2, Rect: domain.Rect{W: 2, H: 2}}}
		scenes <- domain.Scene{Seq: 1, OutputWidth: 2, OutputHeight: 2, Windows: shown}
		r, f := recordingRenderer(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, Options{Width: 2, Height: 2, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, contents.in(), contents.ret, nil, nil)
		}()
		waitFrames(t, f, 1)
		contents.send(domain.SurfaceContent{ID: 1, Seq: 1, SHM: &domain.SHMBuffer{Pool: 1}}, domain.SurfaceContent{ID: 2, Seq: 1, SHM: &domain.SHMBuffer{Pool: 2}})
		waitFrames(t, f, 2)
		if _, c := f.snapshot(); len(c[1]) != 2 || c[1][1].SHM == nil || c[1][2].SHM == nil {
			t.Errorf("batch not applied whole: %+v", c[1])
		}
		select {
		case b := <-contents.returned:
			if len(b.Items) != 0 {
				t.Errorf("batch returned with %d items", len(b.Items))
			}
		case <-time.After(2 * time.Second):
			t.Fatal("batch not returned")
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if len(contents.returned) != 0 {
			t.Error("batch returned more than once")
		}
	})
}

func TestRunFinalizesLastFrameDuration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		scenes := make(chan domain.Scene, 1)
		scenes <- domain.Scene{Seq: 1}
		r, _ := recordingRenderer(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, Options{Width: 2, Height: 2, ScreenshotDir: dir, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, nil, nil, nil, nil)
		}()
		synctest.Wait()
		initial, err := os.ReadFile(filepath.Join(dir, "frames.ffconcat"))
		if err != nil || !strings.Contains(string(initial), "file 'frame-000001.png'") {
			t.Fatalf("frame index not written: %s (%v)", initial, err)
		}
		time.Sleep(50 * time.Millisecond)
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "frames.ffconcat"))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) != 4 || lines[0] != "ffconcat version 1.0" || lines[1] != "file 'frame-000001.png'" || lines[3] != lines[1] {
			t.Fatalf("unexpected finalized index: %s", data)
		}
		duration, err := strconv.ParseFloat(strings.TrimPrefix(lines[2], "duration "), 64)
		if err != nil || duration < 0.05 || math.IsNaN(duration) || math.IsInf(duration, 0) {
			t.Fatalf("final duration: %q (%v)", lines[2], err)
		}
	})
}
