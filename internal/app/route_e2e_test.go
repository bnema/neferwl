package app

import (
	"context"
	"encoding/binary"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bnema.dev/wlturbo"
	extlist "github.com/bnema/go-wayland-bindings/client/extforeigntoplevellist"
	extsource "github.com/bnema/go-wayland-bindings/client/extimagecapturesource"
	extcopy "github.com/bnema/go-wayland-bindings/client/extimagecopycapture"
	clientcore "github.com/bnema/go-wayland-bindings/client/wayland"
	clientxdg "github.com/bnema/go-wayland-bindings/client/xdgshell"
	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// outputReport is what an output reported through the presentation sink: a
// copy, the sink owns the original.
type outputReport struct {
	output string
	seen   map[domain.WindowID]uint64
	shows  map[domain.WindowID]uint64
	flip   bool
}

// routeApp is a headless NeferWL with two outputs (HEADLESS-1 640x480,
// HEADLESS-2 320x240) whose reports and scenes the test reads.
type routeApp struct {
	runtime string
	socket  string
	scenes  chan []domain.Scene
	reports chan outputReport
	control *controlTestClient
	done    chan error
}

//testpolicy:realtime real headless compositor startup and teardown
func startRouteApp(t *testing.T) *routeApp {
	t.Helper()
	for _, k := range []string{"NEFERWL_STATE", "NEFERWL_SLOT", "WAYLAND_DISPLAY"} {
		if v, ok := os.LookupEnv(k); ok {
			_ = os.Unsetenv(k)
			t.Cleanup(func() { _ = os.Setenv(k, v) })
		}
	}
	a := &routeApp{runtime: t.TempDir(), scenes: make(chan []domain.Scene, 4096), reports: make(chan outputReport, 1<<16), done: make(chan error, 1)}
	t.Setenv("XDG_RUNTIME_DIR", a.runtime)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	allowPath := filepath.Join(t.TempDir(), "capture-allow")
	if err := os.WriteFile(allowPath, []byte("*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Animations.On = false
	tap := func(real ports.PresentationSink) ports.PresentationSink {
		sink := portsmocks.NewMockPresentationSink(t)
		sink.EXPECT().Push(mock.Anything).Run(func(r ports.OutputPresented) {
			rep := outputReport{output: r.Output, flip: r.Flip != nil, seen: make(map[domain.WindowID]uint64, len(r.Seen))}
			for id, seq := range r.Seen {
				rep.seen[id] = seq
			}
			if r.Flip != nil {
				rep.shows = make(map[domain.WindowID]uint64, len(r.Flip.Shows))
				for id, seq := range r.Flip.Shows {
					rep.shows[id] = seq
				}
			}
			select {
			case a.reports <- rep:
			default:
				t.Error("report tap full")
			}
			real.Push(r)
		}).Return().Maybe()
		return sink
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		a.done <- Run(ctx, Options{Backend: "headless", NoXwayland: true, PlanOverrides: noTerminal(), Config: cfg, Sizes: [][2]int{{640, 480}, {320, 240}}, testScenes: a.scenes, presentedTap: tap, captureAllowPath: allowPath, captureAllowOwner: uint32(os.Getuid())})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-a.done:
			if err != nil && !strings.Contains(err.Error(), "context canceled") {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Run did not stop")
		}
	})
	awaitSceneOutputs(t, a.scenes, a.done, 2)
	sockets, err := filepath.Glob(filepath.Join(a.runtime, "wayland-*[0-9]"))
	if err != nil || len(sockets) != 1 {
		t.Fatalf("sockets %v: %v", sockets, err)
	}
	a.socket = sockets[0]
	a.control = dialTestControl(t, a.runtime)
	return a
}

// redSide is the side of the client's red buffer.
const redSide = 32

// redClient is a toplevel that commits a red buffer on every frame callback,
// like a client that renders continuously.
type redClient struct {
	commits, frames atomic.Int64
}

// startRedClient runs a real Wayland client against the headless compositor.
func startRedClient(t *testing.T, socket string) *redClient {
	t.Helper()
	c, err := wlturbo.Connect(socket)
	if err != nil {
		t.Fatal(err)
	}
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(c.Roundtrip())
	comp := clientcore.NewCompositor(c.Context())
	shm := clientcore.NewShm(c.Context())
	wm := clientxdg.NewXdgWmBase(c.Context())
	wm.OnPing(func(serial uint32) { _ = wm.Pong(serial) })
	bindTestClient(t, c, clientcore.CompositorInterface, 1, comp)
	bindTestClient(t, c, clientcore.ShmInterface, 1, shm)
	bindTestClient(t, c, clientxdg.XdgWmBaseInterface, 1, wm)
	const side, nbuf = redSide, 3
	fd, err := unix.MemfdCreate("route-client", unix.MFD_CLOEXEC)
	check(err)
	px := make([]byte, side*side*4*nbuf)
	for i := 0; i < len(px); i += 4 {
		binary.LittleEndian.PutUint32(px[i:], 0xffff0000)
	}
	_, err = unix.Write(fd, px)
	check(err)
	pool, err := shm.CreatePool(fd, int32(len(px)))
	check(err)
	var buffers [nbuf]*clientcore.Buffer
	var busy [nbuf]bool
	for i := range buffers {
		buffers[i], err = pool.CreateBuffer(int32(i*side*side*4), side, side, side*4, clientcore.FORMAT_XRGB8888)
		check(err)
		buffers[i].OnRelease(func() { busy[i] = false })
	}
	surf, err := comp.CreateSurface()
	check(err)
	xdg, err := wm.GetXdgSurface(surf)
	check(err)
	configured, permission := false, true
	xdg.OnConfigure(func(serial uint32) { _ = xdg.AckConfigure(serial); configured = true })
	_, err = xdg.GetToplevel()
	check(err)
	check(surf.Commit())
	rc := &redClient{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if configured && permission {
				for i, b := range buffers {
					if busy[i] {
						continue
					}
					if surf.Attach(b, 0, 0) != nil || surf.Damage(0, 0, side, side) != nil {
						return
					}
					cb, err := surf.Frame()
					if err != nil {
						return
					}
					cb.OnDone(func(uint32) { permission = true; rc.frames.Add(1) })
					busy[i], permission = true, false
					if surf.Commit() != nil {
						return
					}
					rc.commits.Add(1)
					break
				}
			}
			if c.Dispatch() != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = c.Close(); <-done })
	return rc
}

// waitWindow waits for a scene of output with exactly one window and returns it.
//
//testpolicy:realtime bounded wait on the real compositor's scenes
func (a *routeApp) waitWindow(t *testing.T, output string) domain.WindowID {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case set := <-a.scenes:
			for _, s := range set {
				if s.Output == output && len(s.Windows) == 1 {
					return s.Windows[0].ID
				}
			}
		case err := <-a.done:
			t.Fatalf("app exited: %v", err)
		case <-deadline:
			t.Fatalf("no window on %s", output)
		}
	}
}

// reportsUntil feeds every report to visit until it returns true.
//
//testpolicy:realtime bounded wait on the real outputs' reports
func (a *routeApp) reportsUntil(t *testing.T, what string, visit func(outputReport) bool) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case r := <-a.reports:
			if visit(r) {
				return
			}
		case err := <-a.done:
			t.Fatalf("app exited waiting for %s: %v", what, err)
		case <-deadline:
			t.Fatalf("timeout waiting for %s", what)
		}
	}
}

// frameFiles lists the frames written for output, oldest first.
func frameFiles(t *testing.T, dir, output string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, output, "frame-*.png"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

// hasWindowPixels reports whether the png at path holds the client's whole
// red 32x32 buffer (drawn at its size): not the background alone.
func hasWindowPixels(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	b, red := img.Bounds(), 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r>>8 > 200 && g>>8 < 60 && bl>>8 < 60 {
				red++
			}
		}
	}
	return red >= redSide*redSide
}

// A window is only sent to the outputs whose scene lists it: the second
// output never reads it, a workspace moved there brings its pixels on the
// first frame, and the first output forgets it.
func TestContentRoutedToListingOutputOnly(t *testing.T) {
	a := startRouteApp(t)
	shots := t.TempDir()
	if err := a.control.call(map[string]any{"op": "shot", "every_frame": true, "dir": shots}); err != nil {
		t.Fatal(err)
	}
	client := startRedClient(t, a.socket)
	id := a.waitWindow(t, "HEADLESS-1")

	// 30 commits later HEADLESS-2 has read nothing of the window.
	secondFlips := 0
	a.reportsUntil(t, "30 commits", func(r outputReport) bool {
		if r.output == "HEADLESS-2" {
			if _, ok := r.seen[id]; ok {
				t.Fatalf("HEADLESS-2 read window %d before it lists it: %v", id, r.seen)
			}
			if r.flip {
				secondFlips++
			}
		}
		return client.commits.Load() >= 30
	})
	idle := len(frameFiles(t, shots, "HEADLESS-2"))

	if err := a.control.call(map[string]any{"op": "do", "action": "move-workspace-to-monitor-right"}); err != nil {
		t.Fatal(err)
	}
	a.waitWindow(t, "HEADLESS-2")
	a.reportsUntil(t, "the window on HEADLESS-2", func(r outputReport) bool {
		_, shown := r.shows[id]
		return r.output == "HEADLESS-2" && r.flip && shown
	})
	if err := a.control.call(map[string]any{"op": "shot", "stop": true}); err != nil {
		t.Fatal(err)
	}
	frames := frameFiles(t, shots, "HEADLESS-2")
	if len(frames) <= idle {
		t.Fatalf("HEADLESS-2 wrote %d frames, %d before the move", len(frames), idle)
	}
	if !hasWindowPixels(t, frames[idle]) {
		t.Fatalf("the first frame HEADLESS-2 presented after the move (%s) has no window pixels", frames[idle])
	}

	// HEADLESS-1 forgets the window once its retire delay passed.
	a.reportsUntil(t, "HEADLESS-1 to retire the window", func(r outputReport) bool {
		_, has := r.seen[id]
		return r.output == "HEADLESS-1" && !has
	})
}

// A window capture of a window that lives on the second output works while
// the first output has focus, and the client keeps getting its buffers back.
func TestWindowCaptureOnOtherOutput(t *testing.T) {
	a := startRouteApp(t)
	if err := a.control.call(map[string]any{"op": "do", "action": "focus-monitor-right"}); err != nil {
		t.Fatal(err)
	}
	client := startRedClient(t, a.socket)
	id := a.waitWindow(t, "HEADLESS-2")
	if err := a.control.call(map[string]any{"op": "do", "action": "focus-monitor-left"}); err != nil {
		t.Fatal(err)
	}

	c, err := wlturbo.Connect(a.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(c.Roundtrip())
	shm := clientcore.NewShm(c.Context())
	list := extlist.NewExtForeignToplevelList(c.Context())
	sources := extsource.NewExtForeignToplevelImageCaptureSourceManager(c.Context())
	copyMgr := extcopy.NewExtImageCopyCaptureManager(c.Context())
	bindTestClient(t, c, clientcore.ShmInterface, 1, shm)
	bindTestClient(t, c, extlist.ExtForeignToplevelListInterface, 1, list)
	bindTestClient(t, c, extsource.ExtForeignToplevelImageCaptureSourceManagerInterface, 1, sources)
	bindTestClient(t, c, extcopy.ExtImageCopyCaptureManagerInterface, 1, copyMgr)
	var handle *extlist.ExtForeignToplevelHandle
	list.OnToplevel(func(h *extlist.ExtForeignToplevelHandle) { handle = h })
	check(c.Roundtrip())
	if handle == nil {
		t.Fatalf("window %d is not listed", id)
	}
	src, err := sources.CreateSource(handle)
	check(err)
	session, err := copyMgr.CreateSession(src, 0)
	check(err)
	var w, h uint32
	done, stopped := false, false
	session.OnBufferSize(func(bw, bh uint32) { w, h = bw, bh })
	session.OnDone(func() { done = true })
	session.OnStopped(func() { stopped = true })
	dispatchUntil(t, c, 10*time.Second, func() string { return "no buffer constraints" }, func() bool { return done || stopped })
	if stopped || w == 0 || h == 0 {
		t.Fatalf("session stopped=%v size %dx%d", stopped, w, h)
	}

	before := client.frames.Load()
	for range 3 {
		fd, err := unix.MemfdCreate("window-capture", unix.MFD_CLOEXEC)
		check(err)
		size := int32(w * h * 4)
		check(unix.Ftruncate(fd, int64(size)))
		pixels, err := unix.Mmap(fd, 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
		check(err)
		pool, err := shm.CreatePool(fd, size)
		check(err)
		buf, err := pool.CreateBuffer(0, int32(w), int32(h), int32(w*4), clientcore.FORMAT_XRGB8888)
		check(err)
		frame, err := session.CreateFrame()
		check(err)
		ready, failed := false, false
		frame.OnReady(func() { ready = true })
		frame.OnFailed(func(uint32) { failed = true })
		check(frame.AttachBuffer(buf))
		check(frame.Capture())
		dispatchUntil(t, c, 10*time.Second, func() string { return "no capture result" }, func() bool { return ready || failed })
		if failed {
			t.Skip("window capture refused (no capture-capable renderer)")
		}
		red := 0
		for i := 0; i+3 < len(pixels); i += 4 {
			if pixels[i+2] > 200 && pixels[i+1] < 60 && pixels[i] < 60 {
				red++
			}
		}
		if red < redSide*redSide {
			t.Fatalf("window capture has %d red pixels of %d: background only", red, len(pixels)/4)
		}
		_ = unix.Munmap(pixels)
		check(frame.Destroy())
	}
	// The client's buffers keep coming back: its frame callbacks continue.
	a.reportsUntil(t, "client frames to continue", func(outputReport) bool { return client.frames.Load() >= before+10 })
}
