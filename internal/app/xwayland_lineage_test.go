//go:build xwayland

package app

import (
	"bnema.dev/wlturbo"
	clientcore "github.com/bnema/go-wayland-bindings/client/wayland"
	clientxdg "github.com/bnema/go-wayland-bindings/client/xdgshell"
	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/statefile"
	"github.com/bnema/neferwl/internal/domain"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestXwaylandLineageCommonRemote(t *testing.T) { runXwaylandLineage(t, false, false) }

func runXwaylandLineage(t *testing.T, direct, seatTV bool) {
	path := filepath.Join(t.TempDir(), "config")
	text := strings.ReplaceAll(keepFocusConfig("on"), "X11Probe", "steam") + "layout.overflow = cascade\nanimations = off\noutput.tv.right-of = HEADLESS-1\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, warnings, err := config.Load(path)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("config: %v %v", err, warnings)
	}
	e := startXwaylandConfig(t, [][2]int{{640, 360}}, func(c *domain.Config) { *c = cfg }, path)
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

	args := []string{"-lineage-controller", "start"}
	if direct {
		args = append(args, "-lineage-direct")
	}
	p := startProbe(t, e.bin, e.display, args...)
	find := func(class string) (statefile.Window, bool) {
		for _, w := range readState(e.statePath).Windows {
			if w.AppID == class {
				return w, true
			}
		}
		return statefile.Window{}, false
	}
	var launcher, game statefile.Window
	t.Cleanup(func() { t.Logf("probe: %s state: %+v", p.out.String(), readState(e.statePath)) })
	until(t, "fullscreen launcher", func() bool {
		var ok bool
		launcher, ok = find("steam")
		return ok && launcher.Output == "tv" && launcher.Fullscreen && strings.Contains(p.out.String(), "mapped 640x360")
	})
	until(t, "launcher active", func() bool { return lastLine(p.out.String(), "active ") == "active self" })
	workspace := func() statefile.Workspace {
		for _, w := range readState(e.statePath).Workspaces {
			if w.Output == "tv" && w.Visible {
				return w
			}
		}
		return statefile.Workspace{}
	}
	desktop := func() statefile.Workspace {
		for _, w := range readState(e.statePath).Workspaces {
			if w.Output == "HEADLESS-1" && w.Visible {
				return w
			}
		}
		return statefile.Workspace{}
	}
	baseline := desktop()
	assertSeat := func() {
		st := readState(e.statePath)
		if !seatTV && (st.Output != "HEADLESS-1" || st.Window == nil || st.Window.AppID != "native") {
			t.Fatalf("seat changed: %+v", st)
		}
		if desktop().ID != baseline.ID {
			t.Fatal("desktop workspace changed")
		}
	}
	typeNative := func() {
		before := len(keys)
		if _, err := io.WriteString(e.script, "key a\n"); err != nil {
			t.Fatal(err)
		}
		waitNative("native key", func() bool { return len(keys) > before })
		if keys[len(keys)-1] != 30 || focused != surface.ID() {
			t.Fatalf("native keyboard: %v focus %d", keys, focused)
		}
		if strings.Contains(p.out.String(), "key\n") {
			t.Fatal("X11 received native key")
		}
		assertSeat()
	}
	assertSeat()
	typeNative()
	if seatTV {
		if _, err := io.WriteString(e.script, "key Super+Ctrl+Right\n"); err != nil {
			t.Fatal(err)
		}
		until(t, "seat on tv", func() bool { return readState(e.statePath).Output == "tv" })
	}
	if _, err := io.WriteString(p.in, "spawn\n"); err != nil {
		t.Fatal(err)
	}
	until(t, "game in selected tv strip", func() bool {
		var ok bool
		game, ok = find("lineage-game")
		w := workspace()
		return ok && game.Output == "tv" && game.Visible && uint64(w.Focused) == game.ID && len(w.Columns) == 2
	})
	w := workspace()
	if len(w.Columns[0].Windows) != 1 || len(w.Columns[1].Windows) != 1 || w.Columns[0].Windows[0].ID != launcher.ID || w.Columns[1].Windows[0].ID != game.ID {
		t.Fatalf("not separate strips: %+v", w)
	}
	r, ok := tileRect(readState(e.statePath), game.ID)
	if !ok || r.Dx() <= 0 || r.Dy() <= 0 || r.Dx() > 640 || r.Dy() > 360 {
		t.Fatalf("game tile: %v", r)
	}
	until(t, "game active", func() bool { return strings.Count(p.out.String(), "active self") >= 2 })
	assertSeat()
	if seatTV {
		until(t, "game keyboard", func() bool { st := readState(e.statePath); return st.Window != nil && st.Window.ID == game.ID })
		mark := strings.Count(p.out.String(), "key\n")
		if _, err := io.WriteString(e.script, "key b\n"); err != nil {
			t.Fatal(err)
		}
		until(t, "X11 game key", func() bool { return strings.Count(p.out.String(), "key\n") > mark })
	} else {
		typeNative()
	}
	if _, err := io.WriteString(p.in, "close\n"); err != nil {
		t.Fatal(err)
	}
	until(t, "launcher restored", func() bool {
		_, exists := find("lineage-game")
		return !exists && uint64(workspace().Focused) == launcher.ID
	})
	until(t, "launcher active again", func() bool { return strings.Count(p.out.String(), "active self") >= 3 })
	launcher, _ = find("steam")
	r, ok = tileRect(readState(e.statePath), launcher.ID)
	if !launcher.Fullscreen || !ok || r.Dx() != 640 || r.Dy() != 360 {
		t.Fatalf("launcher return: %+v %v", launcher, r)
	}
	assertSeat()
	if seatTV {
		until(t, "launcher keyboard", func() bool { st := readState(e.statePath); return st.Window != nil && st.Window.ID == launcher.ID })
	} else {
		typeNative()
	}
}

func TestXwaylandLineageDirectRemote(t *testing.T) { runXwaylandLineage(t, true, false) }

func TestXwaylandLineageTVSeat(t *testing.T) { runXwaylandLineage(t, false, true) }
