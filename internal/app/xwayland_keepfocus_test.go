//go:build xwayland

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bnema.dev/wlturbo"
	lockclient "github.com/bnema/go-wayland-bindings/client/extsessionlock"
	clientcore "github.com/bnema/go-wayland-bindings/client/wayland"
	clientxdg "github.com/bnema/go-wayland-bindings/client/xdgshell"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/domain"
)

// keepFocusConfig is the config file of TestXwaylandKeepFocus: the probe
// opens on the virtual output tv, the native client on HEADLESS-1.
func keepFocusConfig(keep string) string {
	return "xwayland = builtin\nterminal.auto-open = off\n" +
		"output.tv = virtual 640x360\noutput.tv.keep-focus = " + keep + "\n" +
		"rule.steam.app-id = X11Probe\nrule.steam.output = tv\n" +
		"rule.native.app-id = native\nrule.native.output = HEADLESS-1\n"
}

// TestXwaylandKeepFocus: with output.tv.keep-focus, an X11 window on tv keeps
// X11 focus and _NET_ACTIVE_WINDOW while a native window on HEADLESS-1 has
// the keyboard, and typed keys reach the native window only. A session lock
// and turning the key off both take the X11 focus away.
func TestXwaylandKeepFocus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(keepFocusConfig("on")), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, warnings, err := config.Load(path)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("config: %v %v", err, warnings)
	}
	e := startXwaylandConfig(t, [][2]int{{xwaylandW, xwaylandH}}, func(c *domain.Config) { *c = cfg }, path)
	p := startProbe(t, e.bin, e.display, "-focus-log", "-stdin-sync")
	until(t, "probe on tv", func() bool {
		w, ok := windowOf(readState(e.statePath), p.cmd.Process.Pid)
		return ok && w.Output == "tv"
	})
	until(t, "probe X11-active", func() bool { return lastLine(p.out.String(), "active ") == "active self" })

	// A native window opens on HEADLESS-1 and takes the keyboard.
	mark := len(p.out.String())
	socket := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "wayland-1")
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	native, err := wlturbo.Connect(socket)
	check(err)
	t.Cleanup(func() { _ = native.Close() })
	check(native.Roundtrip())
	comp := clientcore.NewCompositor(native.Context())
	shm := clientcore.NewShm(native.Context())
	seat := clientcore.NewSeat(native.Context())
	wm := clientxdg.NewXdgWmBase(native.Context())
	wm.OnPing(func(serial uint32) { check(wm.Pong(serial)) })
	bindTestClient(t, native, clientcore.CompositorInterface, 1, comp)
	bindTestClient(t, native, clientcore.ShmInterface, 1, shm)
	bindTestClient(t, native, clientcore.SeatInterface, 1, seat)
	bindTestClient(t, native, clientxdg.XdgWmBaseInterface, 1, wm)
	keyboard, err := seat.GetKeyboard()
	check(err)
	var focused uint32
	var keys []uint32
	keyboard.OnEnter(func(_ uint32, id uint32, _ []byte) { focused = id })
	keyboard.OnLeave(func(_ uint32, _ uint32) { focused = 0 })
	keyboard.OnKey(func(_, _, key, state uint32) {
		if state == 1 {
			keys = append(keys, key)
		}
	})
	surface, err := comp.CreateSurface()
	check(err)
	xdg, err := wm.GetXdgSurface(surface)
	check(err)
	top, err := xdg.GetToplevel()
	check(err)
	check(top.SetAppId("native"))
	var serial uint32
	xdg.OnConfigure(func(v uint32) { serial = v })
	check(surface.Commit())
	waitNative := func(what string, ready func() bool) {
		t.Helper()
		dispatchUntil(t, native, xwaylandTimeout, func() string { return "missing " + what }, ready)
	}
	waitNative("native configure", func() bool { return serial != 0 })
	check(xdg.AckConfigure(serial))
	check(surface.Attach(sessionLockBuffer(t, shm, 1, 1, clientcore.FORMAT_ARGB8888), 0, 0))
	check(surface.Commit())
	waitNative("native keyboard focus", func() bool { return focused == surface.ID() })
	if w, ok := windowOf(readState(e.statePath), p.cmd.Process.Pid); !ok || w.Output != "tv" {
		t.Fatalf("probe left tv: %+v", w)
	}

	if err := e.script.inject("key", "a"); err != nil {
		t.Fatal(err)
	}
	waitNative("native key", func() bool { return len(keys) != 0 })
	if keys[0] != 30 {
		t.Fatalf("native keys %v, want evdev a=30", keys)
	}
	// The native client has its key; an X round trip by the probe then
	// flushes any X event (a key, a focus change) sent to it before.
	p.sync(t)
	after := p.out.String()[mark:]
	if strings.Contains(after, "focus-out") || strings.Contains(after, "key\n") {
		t.Fatalf("probe lost focus or got a key under native focus: %q", after)
	}
	if got := lastLine(p.out.String(), "active "); got != "active self" {
		t.Fatalf("_NET_ACTIVE_WINDOW %q under native focus, want the probe", got)
	}

	// A session lock takes X11 focus away; the unlock gives it back.
	mark = len(p.out.String())
	locker, err := wlturbo.Connect(socket)
	check(err)
	t.Cleanup(func() { _ = locker.Close() })
	check(locker.Roundtrip())
	manager := lockclient.NewExtSessionLockManager(locker.Context())
	bindTestClient(t, locker, lockclient.ExtSessionLockManagerInterface, 1, manager)
	lock, err := manager.Lock()
	check(err)
	locked := false
	lock.OnLocked(func() { locked = true })
	dispatchUntil(t, locker, xwaylandTimeout, func() string { return "missing locked" }, func() bool { return locked })
	until(t, "probe focus out under lock", func() bool {
		out := p.out.String()[mark:]
		return strings.Contains(out, "focus-out") && lastLine(out, "active ") == "active none"
	})
	mark = len(p.out.String())
	check(lock.UnlockAndDestroy())
	check(locker.Roundtrip())
	until(t, "probe X11-active after unlock", func() bool { return lastLine(p.out.String()[mark:], "active ") == "active self" })

	// Turning keep-focus off on reload takes X11 focus away.
	mark = len(p.out.String())
	if err := os.WriteFile(path, []byte(keepFocusConfig("off")), 0o600); err != nil {
		t.Fatal(err)
	}
	until(t, "probe focus out with keep-focus off", func() bool {
		out := p.out.String()[mark:]
		return strings.Contains(out, "focus-out") && lastLine(out, "active ") == "active none"
	})
}

// lastLine is the last line of out starting with prefix, "" if none.
func lastLine(out, prefix string) string {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], prefix) {
			return lines[i]
		}
	}
	return ""
}
