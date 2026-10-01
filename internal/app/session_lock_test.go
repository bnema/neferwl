package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/vulkan"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/wlturbo"
	clientcore "github.com/bnema/wlturbo/protocol/core"
	lockclient "github.com/bnema/wlturbo/protocol/extsessionlock"
	captureclient "github.com/bnema/wlturbo/protocol/screencopy"
	clientxdg "github.com/bnema/wlturbo/protocol/xdgshell"
	"golang.org/x/sys/unix"
)

// Exercise the production gate, backend proofs and protocol owner together.
// No test-only global registration or injected security events: bufferless
// locked must come from both real headless owners' affirmative black proofs.
func TestHeadlessSessionLockEndToEnd(t *testing.T) {
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	shots := t.TempDir()
	// The test's own capture client is not on the built-in list: disable the check.
	allowPath := filepath.Join(t.TempDir(), "capture-allow")
	if err := os.WriteFile(allowPath, []byte("*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scenes := make(chan []ports.Scene, 128)
	script, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Backend: "headless", NoXwayland: true, NoTerminal: true, Config: config.Defaults(), Sizes: [][2]int{{160, 120}, {128, 96}}, ScreenshotDir: shots, Script: script, testScenes: scenes, captureAllowPath: allowPath, captureAllowOwner: uint32(os.Getuid())})
	}()
	// Cancellation closes clients' sockets too, bounding blocked Roundtrip.
	watchdog := time.AfterFunc(20*time.Second, cancel)
	exited := false
	t.Cleanup(func() {
		watchdog.Stop()
		cancel()
		if exited {
			return
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Run did not stop")
		}
	})
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	waitScenes := func(what string, accept func([]ports.Scene) bool) []ports.Scene {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case set := <-scenes:
				if accept(set) {
					return set
				}
			case err := <-done:
				exited = true
				t.Fatalf("app exited waiting for %s: %v", what, err)
			case <-timer.C:
				t.Fatalf("missing %s scenes", what)
			}
		}
	}
	waitScenes("initial outputs", func(set []ports.Scene) bool { return len(set) == 2 })
	sockets, err := filepath.Glob(filepath.Join(runtime, "wayland-*[0-9]"))
	check(err)
	if len(sockets) != 1 {
		t.Fatalf("sockets: %v", sockets)
	}
	connect := func() *wlturbo.Display {
		t.Helper()
		c, err := wlturbo.Connect(sockets[0])
		check(err)
		t.Cleanup(func() { _ = c.Close() })
		check(c.Roundtrip())
		return c
	}
	until := func(c *wlturbo.Display, what string, ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !ready() {
			if time.Now().After(deadline) {
				t.Fatalf("missing %s", what)
			}
			check(c.Roundtrip())
		}
	}
	desktop := connect()
	// This fails RED until the parent authorizes production advertisement.
	if _, ok := desktop.Registry().FindGlobal(lockclient.ExtSessionLockManagerInterface); !ok {
		t.Fatal("production ext_session_lock_manager_v1 global missing: integration prerequisite")
	}
	comp := clientcore.NewCompositor(desktop.Context())
	shm := clientcore.NewShm(desktop.Context())
	seat := clientcore.NewSeat(desktop.Context())
	wm := clientxdg.NewXdgWmBase(desktop.Context())
	wm.OnPing(func(serial uint32) { check(wm.Pong(serial)) })
	bindTestClient(t, desktop, clientcore.CompositorInterface, 1, comp)
	bindTestClient(t, desktop, clientcore.ShmInterface, 1, shm)
	bindTestClient(t, desktop, clientcore.SeatInterface, 1, seat)
	bindTestClient(t, desktop, clientxdg.XdgWmBaseInterface, 1, wm)
	keyboard, err := seat.GetKeyboard()
	check(err)
	var focused uint32
	var desktopKeys []uint32
	keyboard.OnEnter(func(_ uint32, id uint32, _ []byte) { focused = id })
	keyboard.OnLeave(func(_ uint32, _ uint32) { focused = 0 })
	keyboard.OnKey(func(_, _, key, state uint32) {
		if state == 1 {
			desktopKeys = append(desktopKeys, key)
		}
	})
	surface, err := comp.CreateSurface()
	check(err)
	xdg, err := wm.GetXdgSurface(surface)
	check(err)
	_, err = xdg.GetToplevel()
	check(err)
	var serial uint32
	xdg.OnConfigure(func(value uint32) { serial = value })
	check(surface.Commit())
	until(desktop, "desktop configure", func() bool { return serial != 0 })
	check(xdg.AckConfigure(serial))
	buffer := sessionLockBuffer(t, shm, 1, 1, clientcore.FORMAT_ARGB8888)
	check(surface.Attach(buffer, 0, 0))
	check(surface.Commit())
	until(desktop, "desktop focus", func() bool { return focused == surface.ID() })
	_, err = io.WriteString(writer, "key a\n")
	check(err)
	until(desktop, "initial desktop input", func() bool { return len(desktopKeys) != 0 })

	var outputs []*clientcore.Output
	for name, global := range desktop.Registry().GetGlobals() {
		if global.Interface != clientcore.OutputInterface {
			continue
		}
		out := clientcore.NewOutput(desktop.Context())
		check(desktop.Registry().Bind(name, global.Interface, 4, out))
		outputs = append(outputs, out)
	}
	if len(outputs) != 2 {
		t.Fatalf("outputs: %d", len(outputs))
	}
	captures := captureclient.NewScreencopyManager(desktop.Context())
	bindTestClient(t, desktop, captureclient.ScreencopyManagerInterface, 3, captures)
	denyCapture := func() {
		t.Helper()
		for _, out := range outputs {
			frame, err := captures.CaptureOutputRegion(0, out, 0, 0, 1, 1)
			check(err)
			failed, ready := false, false
			frame.OnFailed(func() { failed = true })
			frame.OnReady(func(_, _, _ uint32) { ready = true })
			frame.OnBufferDone(func() { check(frame.Copy(sessionLockBuffer(t, shm, 1, 1, clientcore.FORMAT_XRGB8888))) })
			until(desktop, "capture refusal", func() bool { return failed || ready })
			if !failed || ready {
				t.Fatal("ordinary capture succeeded while protected")
			}
			check(frame.Destroy())
		}
	}
	// Positive control: the same ordinary client and valid SHM destination
	// can capture before acquisition, so refusal is not a malformed request.
	// Software Vulkan without sync-file export (lavapipe) refuses every
	// protocol capture by design, so the control only holds where the device
	// can capture; the protection assertions below still run everywhere.
	if captureSupported(t) {
		control, err := captures.CaptureOutputRegion(0, outputs[0], 0, 0, 1, 1)
		check(err)
		controlReady, controlFailed := false, false
		control.OnReady(func(_, _, _ uint32) { controlReady = true })
		control.OnFailed(func() { controlFailed = true })
		control.OnBufferDone(func() { check(control.Copy(sessionLockBuffer(t, shm, 1, 1, clientcore.FORMAT_XRGB8888))) })
		until(desktop, "unlocked capture control", func() bool { return controlReady || controlFailed })
		if !controlReady || controlFailed {
			t.Fatal("valid unlocked capture failed")
		}
		check(control.Destroy())
	} else {
		t.Log("Vulkan device cannot capture (no sync-file export): positive control skipped")
	}
	// Protocol focus/capture completion is not completion of asynchronous PNG
	// writes. Establish both screenshot baselines before engaging protection.
	until(desktop, "initial screenshots on both outputs", func() bool {
		for _, name := range []string{"HEADLESS-1", "HEADLESS-2"} {
			if _, err := os.Stat(filepath.Join(shots, name, "latest.png")); err != nil {
				return false
			}
		}
		return true
	})
	acquire := func(c *wlturbo.Display) *lockclient.ExtSessionLock {
		t.Helper()
		manager := lockclient.NewExtSessionLockManager(c.Context())
		bindTestClient(t, c, lockclient.ExtSessionLockManagerInterface, 1, manager)
		lock, err := manager.Lock()
		check(err)
		locked, finished := 0, false
		lock.OnLocked(func() { locked++ })
		lock.OnFinished(func() { finished = true })
		until(c, "bufferless locked", func() bool { return locked != 0 || finished })
		if locked != 1 || finished {
			t.Fatalf("lock result: locked=%d finished=%v", locked, finished)
		}
		check(c.Roundtrip())
		if locked != 1 {
			t.Fatalf("duplicate locked: %d", locked)
		}
		return lock
	}
	locker := connect()
	lock := acquire(locker) // deliberately no locker surfaces or buffers yet
	protected := waitScenes("protected black", func(set []ports.Scene) bool {
		if len(set) != 2 {
			return false
		}
		for _, s := range set {
			if !s.Security.Protected || len(s.Windows) != 0 || len(s.Layers) != 0 || s.Background != "#000000" || s.Capture != nil || s.CaptureScene != nil {
				return false
			}
		}
		return true
	})
	generation := protected[0].Security.Generation
	if generation == 0 || protected[1].Security.Generation != generation {
		t.Fatal("incoherent protection generation")
	}
	check(desktop.Roundtrip())
	if focused != 0 {
		t.Fatal("desktop retained keyboard focus under lock")
	}
	denyCapture()
	before := sessionLockShots(t, shots)
	for _, name := range []string{"HEADLESS-1", "HEADLESS-2"} {
		if _, ok := before[filepath.Join(shots, name, "latest.png")]; !ok {
			t.Fatalf("missing initial screenshot for %s", name)
		}
	}

	// A configured real locker surface supplies an input completion witness:
	// receipt by the locker proves scripted key processing without timing sleeps.
	lockComp := clientcore.NewCompositor(locker.Context())
	lockShm := clientcore.NewShm(locker.Context())
	lockSeat := clientcore.NewSeat(locker.Context())
	bindTestClient(t, locker, clientcore.CompositorInterface, 1, lockComp)
	bindTestClient(t, locker, clientcore.ShmInterface, 1, lockShm)
	bindTestClient(t, locker, clientcore.SeatInterface, 1, lockSeat)
	global, ok := locker.Registry().FindGlobal(clientcore.OutputInterface)
	if !ok {
		t.Fatal("locker output missing")
	}
	lockOutput := clientcore.NewOutput(locker.Context())
	check(locker.Registry().Bind(global.Name, global.Interface, 4, lockOutput))
	lockSurface, err := lockComp.CreateSurface()
	check(err)
	role, err := lock.GetLockSurface(lockSurface, lockOutput)
	check(err)
	configured := false
	role.OnConfigure(func(serial, width, height uint32) {
		check(role.AckConfigure(serial))
		check(lockSurface.Attach(sessionLockBuffer(t, lockShm, int32(width), int32(height), clientcore.FORMAT_ARGB8888), 0, 0))
		check(lockSurface.Commit())
		configured = true
	})
	lockKeyboard, err := lockSeat.GetKeyboard()
	check(err)
	lockFocused, lockKey, lockReleased := false, false, false
	lockKeyboard.OnEnter(func(_ uint32, id uint32, _ []byte) { lockFocused = id == lockSurface.ID() })
	lockKeyboard.OnKey(func(_, _, key, state uint32) {
		if key != 48 {
			return
		} // evdev b
		if state == 1 {
			lockKey = true
		} else {
			lockReleased = true
		}
	})
	until(locker, "locker focus", func() bool { return configured && lockFocused })
	desktopCount := len(desktopKeys)
	_, err = io.WriteString(writer, "key b\n")
	check(err)
	until(locker, "locker input and release", func() bool { return lockKey && lockReleased })
	check(desktop.Roundtrip())
	if len(desktopKeys) != desktopCount {
		t.Fatal("locker input leaked to desktop")
	}
	if after := sessionLockShots(t, shots); !equalSessionLockShots(before, after) {
		t.Fatal("screenshots changed while protected")
	}

	check(locker.Close()) // crash never releases the gate
	denyCapture()
	replacement := connect()
	takeover := acquire(replacement)
	newer := waitScenes("takeover protection", func(set []ports.Scene) bool {
		return len(set) == 2 && set[0].Security.Protected && set[1].Security.Protected && set[0].Security.Generation > generation
	})
	if newer[0].Security.Generation != newer[1].Security.Generation {
		t.Fatal("takeover generation differs across outputs")
	}
	check(takeover.UnlockAndDestroy())
	check(replacement.Roundtrip())
	waitScenes("unlocked desktop", func(set []ports.Scene) bool {
		return len(set) == 2 && !set[0].Security.Protected && !set[1].Security.Protected && set[0].Security.Generation > newer[0].Security.Generation
	})
	until(desktop, "restored desktop focus", func() bool { return focused == surface.ID() })
	_, err = io.WriteString(writer, "key c\n")
	check(err)
	until(desktop, "restored desktop input", func() bool { return len(desktopKeys) > desktopCount })
	if desktopKeys[len(desktopKeys)-1] != 46 {
		t.Fatalf("restored key: %v, want evdev c=46", desktopKeys)
	}
}

// Real protocol SHM is a client buffer, not a renderer fallback or a double.
func sessionLockBuffer(t *testing.T, shm *clientcore.Shm, width, height int32, format uint32) *clientcore.Buffer {
	t.Helper()
	size := width * height * 4
	fd, err := unix.MemfdCreate("session-lock-client", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		unix.Close(fd)
		t.Fatal(err)
	}
	pool, err := shm.CreatePool(fd, size) // generated request consumes fd on success
	if err != nil {
		unix.Close(fd)
		t.Fatal(err)
	}
	buffer, err := pool.CreateBuffer(0, width, height, width*4, format)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Destroy(); err != nil {
		t.Fatal(err)
	}
	return buffer
}

func sessionLockShots(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "HEADLESS-*", "*.png"))
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string][]byte, len(files))
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		result[path] = data
	}
	return result
}

func equalSessionLockShots(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for path, data := range a {
		if !bytes.Equal(data, b[path]) {
			return false
		}
	}
	return true
}

// captureSupported probes the device the headless outputs will render on.
func captureSupported(t *testing.T) bool {
	t.Helper()
	r, err := vulkan.New(16, 16)
	if err != nil {
		return false
	}
	defer r.Close()
	return r.CaptureSupported()
}
