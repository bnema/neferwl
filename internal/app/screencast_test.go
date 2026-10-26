//go:build screencast

package app

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerobus"
)

// The screencast tests need a session bus of their own (run them under
// dbus-run-session), the user's PipeWire and gst-launch-1.0 as the consumer.

const (
	scName    = "org.gnome.Mutter.ScreenCast"
	scPath    = "/org/gnome/Mutter/ScreenCast"
	scSession = "org.gnome.Mutter.ScreenCast.Session"
	scStream  = "org.gnome.Mutter.ScreenCast.Stream"
	scDenied  = "org.freedesktop.DBus.Error.AccessDenied"
)

// castApp is a headless NeferWL serving the D-Bus services.
type castApp struct {
	input chan<- ports.InputEvent
	stop  func()
}

func requireCastEnv(t *testing.T) {
	t.Helper()
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		t.Skip("no session bus: run under dbus-run-session")
	}
	if _, err := exec.LookPath("gst-launch-1.0"); err != nil {
		t.Skip("gst-launch-1.0 unavailable")
	}
	if err := exec.Command("pw-dump").Run(); err != nil {
		t.Skipf("PipeWire is not running: %v", err)
	}
}

// startCastApp runs the compositor with the capture allowlist content. The
// PipeWire socket stays where it is: only the Wayland socket and the state
// move to temporary directories.
//
//testpolicy:realtime real headless renderer and D-Bus service startup and teardown
func startCastApp(t *testing.T, allow string) *castApp {
	t.Helper()
	t.Setenv("PIPEWIRE_RUNTIME_DIR", os.Getenv("XDG_RUNTIME_DIR"))
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	allowPath := filepath.Join(t.TempDir(), "capture-allow")
	if err := os.WriteFile(allowPath, []byte(allow), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	inputReady := make(chan chan<- ports.InputEvent, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, Options{Backend: "headless", DBus: true, NoXwayland: true, NoTerminal: true, Config: config.Defaults(), Sizes: [][2]int{{320, 240}}, captureAllowPath: allowPath, captureAllowOwner: uint32(os.Getuid())},
			func(in chan<- ports.InputEvent) { inputReady <- in })
	}()
	a := &castApp{}
	select {
	case a.input = <-inputReady:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("app did not start")
	}
	a.stop = func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !strings.Contains(err.Error(), "context canceled") {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Run did not stop")
		}
	}
	t.Cleanup(a.stop)
	return a
}

// castClient is a client of the D-Bus services.
func castClient(t *testing.T) *zerobus.Conn {
	t.Helper()
	c, err := zerobus.SessionBus()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

//testpolicy:realtime D-Bus NameOwnerChanged is emitted by a real session bus
func waitOwner(t *testing.T, c *zerobus.Conn, name string) {
	t.Helper()
	rule := "type='signal',interface='org.freedesktop.DBus',member='NameOwnerChanged',arg0='" + name + "'"
	if err := c.AddMatch(rule); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.RemoveMatch(rule) }()
	c.NewCall("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "NameHasOwner", "s").Str(name)
	m, err := c.Call()
	if err != nil {
		t.Fatal(err)
	}
	if m.Body().Bool() {
		return
	}
	timeout := time.AfterFunc(15*time.Second, func() { _ = c.Close() })
	defer timeout.Stop()
	for {
		m, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("waiting for %s: %v", name, err)
		}
		if m.Member == "NameOwnerChanged" {
			body := m.Body()
			if body.Str() == name {
				_ = body.Str()
				if body.Str() != "" {
					return
				}
			}
		}
	}
}

func emptyProps(e *zerobus.Encoder) { e.EndArray(e.BeginArray('{')) }

// call returns the error name, "" on success.
func call(c *zerobus.Conn, path, iface, member, sig string, body func(*zerobus.Encoder)) (string, *zerobus.Message) {
	e := c.NewCall(scName, path, iface, member, sig)
	if body != nil {
		body(e)
	}
	m, err := c.Call()
	if err == nil {
		return "", m
	}
	if de, ok := err.(*zerobus.Error); ok {
		return de.Name, m
	}
	return err.Error(), m
}

func recordMonitor(c *zerobus.Conn, session, connector string) (string, string) {
	name, m := call(c, session, scSession, "RecordMonitor", "sa{sv}", func(e *zerobus.Encoder) {
		e.Str(connector)
		a := e.BeginArray('{')
		e.Struct()
		e.Str("cursor-mode")
		e.Variant("u")
		e.Uint32(1)
		e.EndArray(a)
	})
	if name != "" {
		return name, ""
	}
	return "", strings.Clone(m.Body().ObjectPath())
}

// A monitor cast streams live to a PipeWire consumer, allocates next to
// nothing per frame, refuses another client's calls and ends on Stop.
//
//testpolicy:realtime real PipeWire/GStreamer frames and D-Bus lifecycle
func TestScreencastStreamsMonitor(t *testing.T) {
	requireCastEnv(t)
	app := startCastApp(t, "*\n")
	c := castClient(t)
	waitOwner(t, c, scName)
	// The inventory reaches the service after the outputs appear.
	var session, stream string
	timeout, tick := time.After(15*time.Second), time.NewTicker(50*time.Millisecond)
	defer tick.Stop()
	for {
		name, m := call(c, scPath, "org.gnome.Mutter.ScreenCast", "CreateSession", "a{sv}", emptyProps)
		if name != "" {
			t.Fatalf("CreateSession: %s", name)
		}
		session = strings.Clone(m.Body().ObjectPath())
		var errName string
		if errName, stream = recordMonitor(c, session, "HEADLESS-1"); errName == "" {
			break
		}
		call(c, session, scSession, "Stop", "", nil)
		select {
		case <-timeout:
			t.Fatalf("RecordMonitor: %s", errName)
		case <-tick.C:
		}
	}
	rule := "type='signal',interface='" + scStream + "'"
	if err := c.AddMatch(rule); err != nil {
		t.Fatal(err)
	}
	if err := c.AddMatch("type='signal',interface='" + scSession + "'"); err != nil {
		t.Fatal(err)
	}
	if name, _ := call(c, session, scSession, "Start", "", nil); name != "" {
		t.Fatalf("Start: %s", name)
	}
	node := uint32(0)
	for node == 0 {
		m, err := c.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if m.Member == "PipeWireStreamAdded" && m.Path == stream && m.Destination == c.UniqueName() {
			node = m.Body().Uint32()
		}
	}

	// Pointer motion damages the output while the consumer runs. The events
	// are boxed once: sending them allocates nothing.
	stopMoving := make(chan struct{})
	var stopOnce sync.Once
	stopMotion := func() { stopOnce.Do(func() { close(stopMoving) }) }
	a := ports.InputEvent(ports.SecurityInput{Event: ports.PointerMotion{X: 40, Y: 40}})
	b := ports.InputEvent(ports.SecurityInput{Event: ports.PointerMotion{X: 200, Y: 160}})
	moved := make(chan struct{})
	go func() {
		defer close(moved)
		tick := time.NewTicker(16 * time.Millisecond)
		defer tick.Stop()
		for i := 0; ; i++ {
			ev := a
			if i%2 == 1 {
				ev = b
			}
			// Neither the send nor the wait may outlive the test: the app
			// may have stopped reading.
			select {
			case app.input <- ev:
			case <-stopMoving:
				return
			}
			select {
			case <-tick.C:
			case <-stopMoving:
				return
			}
		}
	}()
	defer func() { stopMotion(); <-moved }()

	gst := exec.Command("gst-launch-1.0", "-v", "pipewiresrc", "path="+itoa(node), "stream-properties=props,media.type=Video", "num-buffers=300", "!", "fakesink", "silent=false")
	out, err := gst.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := gst.Start(); err != nil {
		t.Fatal(err)
	}
	var frames atomic.Int64
	frameSeen := make(chan struct{}, 1)
	go func() {
		// Counted on raw reads: a line per frame would allocate a string.
		buf := make([]byte, 64<<10)
		for {
			n, err := out.Read(buf)
			frames.Add(int64(bytes.Count(buf[:n], []byte("chain "))))
			select {
			case frameSeen <- struct{}{}:
			default:
			}
			if err != nil {
				return
			}
		}
	}()
	exited := make(chan error, 1)
	go func() { exited <- gst.Wait() }()

	// Steady state: measure 150 frames after 60 warm-up frames. Every allocation is
	// recorded (MemProfileRate 1) and attributed by its stack: the cast path
	// (screencast, wayland casts, purego-pipewire, zerobus) must allocate
	// less than once per frame, and so must the renderer capture path it
	// shares with ext-image-copy-capture (BeginCapture, EndCapture and the
	// pipeline worker). The headless output's own display frame (Render's
	// sync file, the flip report, injected input) allocates whether or not
	// a cast runs: it is reported in the whole-process figure, not asserted.
	waitFrames(t, &frames, frameSeen, exited, 60)
	runtime.MemProfileRate = 1
	f0 := frames.Load()
	before := allocSnapshot()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	waitFrames(t, &frames, frameSeen, exited, f0+150)
	runtime.ReadMemStats(&m1)
	after := allocSnapshot()
	f1 := frames.Load()
	runtime.MemProfileRate = 512 * 1024
	n := f1 - f0
	if n < 60 {
		t.Errorf("only %d frames in measurement interval", n)
	} else {
		cast, other := after.sub(before)
		per := float64(cast) / float64(n)
		t.Logf("%d frames: cast path %.3f mallocs per frame, whole process %.2f (renderer and runtime included; %d other profiled)",
			n, per, float64(m1.Mallocs-m0.Mallocs)/float64(n), other)
		if per >= 1 {
			t.Errorf("cast path: %.2f mallocs per frame over %d frames, want < 1", per, n)
		}
	}

	// The consumer got its 300 buffers and left.
	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("gst-launch-1.0: %v", err)
		}
	case <-time.After(60 * time.Second):
		_ = gst.Process.Kill()
		t.Fatal("gst-launch-1.0 did not finish")
	}
	stopMotion()

	// A second client is another unique name: it is allowed (the list is
	// off) but the session is not its own.
	other := castClient(t)
	if name, _ := call(other, session, scSession, "Stop", "", nil); name != scDenied {
		t.Errorf("Stop by another client: %q", name)
	}
	if name, _ := call(other, stream, "org.freedesktop.DBus.Properties", "GetAll", "s", func(e *zerobus.Encoder) { e.Str(scStream) }); name != scDenied {
		t.Errorf("stream GetAll by another client: %q", name)
	}
	// A window that is not mapped cannot be recorded.
	if name, _ := call(c, session, scSession, "RecordWindow", "a{sv}", func(e *zerobus.Encoder) {
		a := e.BeginArray('{')
		e.Struct()
		e.Str("window-id")
		e.Variant("t")
		e.Uint64(99999)
		e.EndArray(a)
	}); name == "" {
		t.Error("RecordWindow of a closed window succeeded")
	}
	if name, _ := call(c, session, scSession, "Stop", "", nil); name != "" {
		t.Fatalf("Stop: %s", name)
	}
	for closed := false; !closed; {
		m, err := c.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		closed = m.Member == "Closed" && m.Path == session
	}
}

// A caller whose executable is not on the list gets AccessDenied from
// every object, and nothing is captured.
func TestScreencastAllowlistDenies(t *testing.T) {
	requireCastEnv(t)
	startCastApp(t, "/usr/bin/grim\n")
	c := castClient(t)
	waitOwner(t, c, scName)
	if name, _ := call(c, scPath, "org.gnome.Mutter.ScreenCast", "CreateSession", "a{sv}", emptyProps); name != scDenied {
		t.Errorf("CreateSession: %q", name)
	}
	if name, _ := call(c, scPath, "org.freedesktop.DBus.Properties", "GetAll", "s", func(e *zerobus.Encoder) { e.Str("org.gnome.Mutter.ScreenCast") }); name != scDenied {
		t.Errorf("GetAll: %q", name)
	}
	for _, x := range []struct{ dest, path, iface, member string }{
		{"org.gnome.Mutter.DisplayConfig", "/org/gnome/Mutter/DisplayConfig", "org.gnome.Mutter.DisplayConfig", "GetCurrentState"},
		{"org.gnome.Shell.Introspect", "/org/gnome/Shell/Introspect", "org.gnome.Shell.Introspect", "GetWindows"},
		{"org.gnome.Shell.Screenshot", "/org/gnome/Shell/Screenshot", "org.gnome.Shell.Screenshot", "InteractiveScreenshot"},
	} {
		e := c.NewCall(x.dest, x.path, x.iface, x.member, "")
		_ = e
		if _, err := c.Call(); err == nil || !strings.Contains(err.Error(), scDenied) {
			t.Errorf("%s: %v", x.member, err)
		}
	}
}

func itoa(n uint32) string { return strconv.FormatUint(uint64(n), 10) }

// castPath names the packages whose allocations the cast path owns.
var castPath = []string{
	"internal/adapters/screencast.",
	"purego-pipewire",
	"bnema/zerobus",
	"wayland.(*castSession)",
	"wayland.(*Server).requestCast",
	"wayland.(*Server).castFlip",
	"wayland.(*Server).forwardCasts",
	"wayland.(*Server).forwardCaptured",
	"wayland.postRing",
	"outputkit/capture.writeFrame",
	// The renderer capture path, shared with ext-image-copy-capture and
	// screencopy: lease, copy, read, return.
	"vulkan.(*Renderer).BeginCapture",
	"vulkan.(*Renderer).EndCapture",
	"outputkit/capture.(*Pipeline).SubmitScoped",
	"outputkit/capture.(*Pipeline).process",
	"outputkit/capture.(*Pipeline).Recycle",
}

// allocs counts profiled allocation objects by stack, for the cast path and
// for the rest.
type allocs struct {
	cast, other map[string]int64
}

func allocSnapshot() allocs {
	runtime.GC() // the memory profile is as of the last completed GC
	var recs []runtime.MemProfileRecord
	for {
		recs = make([]runtime.MemProfileRecord, 4096)
		n, ok := runtime.MemProfile(recs, true)
		if ok {
			recs = recs[:n]
			break
		}
		recs = make([]runtime.MemProfileRecord, n+256)
		if n, ok = runtime.MemProfile(recs, true); ok {
			recs = recs[:n]
			break
		}
	}
	a := allocs{cast: map[string]int64{}, other: map[string]int64{}}
	for i := range recs {
		pcs := recs[i].Stack()
		frames := runtime.CallersFrames(pcs)
		var key strings.Builder
		isCast := false
		for {
			f, more := frames.Next()
			key.WriteString(f.Function)
			key.WriteByte(';')
			for _, p := range castPath {
				if strings.Contains(f.Function, p) {
					isCast = true
				}
			}
			if !more {
				break
			}
		}
		if isCast {
			a.cast[key.String()] += recs[i].AllocObjects
		} else {
			a.other[key.String()] += recs[i].AllocObjects
		}
	}
	return a
}

// sub returns the objects allocated between two snapshots.
func (a allocs) sub(b allocs) (cast, other int64) {
	for k, v := range a.cast {
		cast += v - b.cast[k]
	}
	for k, v := range a.other {
		other += v - b.other[k]
	}
	return cast, other
}

//testpolicy:realtime observes frames from the real GStreamer/PipeWire consumer
func waitFrames(t *testing.T, frames *atomic.Int64, changed <-chan struct{}, exited <-chan error, target int64) {
	t.Helper()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for frames.Load() < target {
		select {
		case <-changed:
		case err := <-exited:
			t.Fatalf("consumer exited before frame %d: %v", target, err)
		case <-timer.C:
			t.Fatalf("consumer stalled at frame %d, want %d", frames.Load(), target)
		}
	}
}
