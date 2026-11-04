//go:build xwayland

// End-to-end test of the built-in XWM: a real Xwayland and a real X11 client
// (testdata/x11probe) run against a headless NeferWL. It needs Xwayland on
// PATH and a GPU, so it is excluded from `go test ./...` and CI. Run it with
// `make e2e-xwayland`.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"github.com/bnema/neferwl/internal/domain"
	"golang.org/x/sys/unix"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"bnema.dev/zerowrap"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/statefile"
)

const (
	xwaylandTimeout      = 10 * time.Second
	xwaylandW, xwaylandH = 640, 360
)

var xwaylandPublications sync.Map // test -> notification from real output/filesystem I/O

var displayRe = regexp.MustCompile(`"DISPLAY":"(:\d+)"`)

// probe is a running x11probe.
type probe struct {
	cmd    *exec.Cmd
	out    *lockedBuffer
	waited chan error
}

// xwaylandEnv is a running headless compositor with the built-in XWM.
type xwaylandEnv struct {
	bin, display, statePath, shots string
	script                         *io.PipeWriter
}

// startXwayland runs the compositor on one headless output and waits for its
// X11 display. It skips the test without Xwayland and stops everything when
// the test ends.
func startXwayland(t *testing.T) *xwaylandEnv {
	t.Helper()
	return startXwaylandWith(t, [][2]int{{xwaylandW, xwaylandH}}, nil)
}

// startXwaylandWith is startXwayland with the given headless outputs, left to
// right, and an optional change to the default config.
func startXwaylandWith(t *testing.T, sizes [][2]int, mutate func(*domain.Config)) *xwaylandEnv {
	t.Helper()
	return startXwaylandBackend(t, domain.BackendHeadless, sizes, mutate)
}

// startXwaylandBackend is startXwaylandWith on backend. The server backend
// never runs libinput here (noLibinput): the script stays the input.
//
//testpolicy:realtime bounded wait on real X11/Wayland sockets or child process I/O
func startXwaylandBackend(t *testing.T, backend domain.Backend, sizes [][2]int, mutate func(*domain.Config)) *xwaylandEnv {
	t.Helper()
	if _, err := exec.LookPath("Xwayland"); err != nil {
		t.Skip("Xwayland not on PATH")
	}
	// The compositor must not read or write the live session's state.
	for _, k := range []string{"NEFERWL_STATE", "NEFERWL_SLOT"} {
		if v, ok := os.LookupEnv(k); ok {
			os.Unsetenv(k)
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	shots := t.TempDir()
	bin := buildProbe(t)

	script, scriptW := io.Pipe()
	changes := make(chan struct{}, 1)
	xwaylandPublications.Store(t, changes)
	t.Cleanup(func() { xwaylandPublications.Delete(t) })
	logs := &lockedBuffer{changed: changes}
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(runtime, "neferwl")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{runtime, stateDir, shots} {
		if _, err := unix.InotifyAddWatch(fd, dir, unix.IN_CREATE|unix.IN_CLOSE_WRITE|unix.IN_MOVED_TO); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { unix.Close(fd) })
	watchDone := make(chan struct{})
	watchExited := make(chan struct{})
	t.Cleanup(func() { close(watchDone); <-watchExited })
	go func() {
		defer close(watchExited)
		var b [16384]byte
		for {
			select {
			case <-watchDone:
				return
			default:
			}
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			n, err := unix.Poll(fds, 100)
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				return
			}
			if n == 0 {
				continue
			}
			if _, err := unix.Read(fd, b[:]); err != nil {
				if err == unix.EAGAIN || err == unix.EINTR {
					continue
				}
				return
			}
			select {
			case changes <- struct{}{}:
			default:
			}
		}
	}()

	ctx, cancel := context.WithCancel(zerowrap.WithCtx(context.Background(), zerowrap.New(zerowrap.Config{Level: "info", Format: "json", Output: logs})))
	cfg := config.Defaults()
	cfg.Xwayland = domain.XwaylandConfig{Backend: domain.XwaylandBuiltin, Binary: "Xwayland", XSettings: true}
	if mutate != nil {
		mutate(&cfg)
	}
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Backend: backend, NoTerminal: true, Config: cfg, Script: script,
			ScreenshotDir: shots, Sizes: sizes, noLibinput: backend == domain.BackendServer,
		})
	}()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("compositor log:\n%s", logs.String())
		}
		cancel()
		_ = scriptW.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Run did not stop")
		}
	})

	var display string
	until(t, "X11 display in the log", func() bool {
		if m := displayRe.FindStringSubmatch(logs.String()); m != nil {
			display = m[1]
			return true
		}
		return false
	})
	statePath, err := statefile.Path(runtime, "wayland-1")
	if err != nil {
		t.Fatal(err)
	}
	return &xwaylandEnv{bin: bin, display: display, statePath: statePath, shots: shots, script: scriptW}
}

// TestXwaylandProbe: an X11 client is managed by the built-in XWM. It shows
// up in the state with its class and pid, is drawn, tiles next to a second
// one, receives focus and is closed by the close-window bind.
//
//testpolicy:realtime bounded wait on real X11/Wayland sockets or child process I/O
func TestXwaylandProbe(t *testing.T) {
	e := startXwayland(t)
	bin, display, statePath, shots, scriptW := e.bin, e.display, e.statePath, e.shots, e.script
	first := startProbe(t, bin, display, "-title", "one")
	until(t, "probe one mapped", func() bool { return strings.Contains(first.out.String(), "mapped ") })

	var st statefile.State
	until(t, "one visible X11Probe window", func() bool {
		st = readState(statePath)
		return len(st.Windows) == 1 && st.Windows[0].AppID == "X11Probe" && st.Windows[0].Visible
	})
	if !st.Windows[0].Xwayland {
		t.Errorf("built-in X11 window not marked Xwayland: %+v", st.Windows[0])
	}
	if w := st.Windows[0]; w.PID != first.cmd.Process.Pid {
		t.Errorf("window pid %d, want %d", w.PID, first.cmd.Process.Pid)
	}

	// The client draws green and the compositor shows it.
	id := st.Windows[0].ID
	until(t, "green window in latest.png", func() bool {
		r, ok := tileRect(readState(statePath), id)
		return ok && greenShare(filepath.Join(shots, "latest.png"), r) >= 0.5
	})

	// Focus round trip: the compositor focused the window, the client saw it.
	until(t, "probe one focus", func() bool { return strings.Contains(first.out.String(), "focus") })

	// A second probe tiles beside the first.
	second := startProbe(t, bin, display, "-title", "two")
	until(t, "two tiled windows", func() bool {
		st = readState(statePath)
		if len(st.Windows) != 2 {
			return false
		}
		for _, w := range st.Windows {
			if w.AppID != "X11Probe" || !w.Visible || w.Floating {
				return false
			}
		}
		return true
	})
	until(t, "probe two focus", func() bool { return strings.Contains(second.out.String(), "focus") })

	// The close-window bind asks the focused (second) probe to close.
	if _, err := io.WriteString(scriptW, "key Super+q\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-second.waited:
		if err != nil {
			t.Errorf("probe two exited with %v, want 0", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("probe two did not exit within 2s of Super+q")
	}
	until(t, "one window left", func() bool {
		st = readState(statePath)
		return len(st.Windows) == 1 && st.Windows[0].PID == first.cmd.Process.Pid
	})
	select {
	case err := <-first.waited:
		t.Fatalf("probe one exited (%v) though only the focused probe was closed", err)
	default:
	}
}

// buildProbe builds testdata/x11probe.
func buildProbe(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "x11probe")
	build := exec.Command("go", "build", "-o", bin, "./testdata/x11probe")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build x11probe: %v: %s", err, out)
	}
	return bin
}

// startProbe starts x11probe on display, with X11 as its only display
// server, and stops it when the test ends.
//
//testpolicy:realtime bounded wait on real X11/Wayland sockets or child process I/O
func startProbe(t *testing.T, bin, display string, args ...string) *probe {
	t.Helper()
	changes := publicationChanges(t)
	p := &probe{out: &lockedBuffer{changed: changes}, waited: make(chan error, 1)}
	p.cmd = exec.Command(bin, args...)
	p.cmd.Env = append(envWithout("DISPLAY", "WAYLAND_DISPLAY", "NEFERWL_STATE", "NEFERWL_SLOT"), "DISPLAY="+display)
	p.cmd.Stdout = p.out
	stderr := &lockedBuffer{}
	p.cmd.Stderr = stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var exited sync.Once
	go func() { p.waited <- p.cmd.Wait() }()
	t.Cleanup(func() {
		exited.Do(func() {
			_ = p.cmd.Process.Kill()
			select {
			case <-p.waited:
			case <-time.After(2 * time.Second):
			}
		})
		if s := stderr.String(); s != "" {
			t.Logf("x11probe stderr: %s", s)
		}
	})
	return p
}

func envWithout(keys ...string) []string {
	var env []string
next:
	for _, kv := range os.Environ() {
		for _, k := range keys {
			if strings.HasPrefix(kv, k+"=") {
				continue next
			}
		}
		env = append(env, kv)
	}
	return env
}

// until waits for real process output or filesystem publication before rechecking.
//
//testpolicy:realtime waits on real compositor output and inotify publications
func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	changes := publicationChanges(t)
	deadline := time.After(xwaylandTimeout)
	for !cond() {
		select {
		case <-changes:
		case <-deadline:
			t.Fatalf("timeout waiting for %s", what)
		}
	}
}

// readState returns the current state file, or an empty state while it does
// not exist or is being replaced.
func readState(path string) statefile.State {
	st, err := statefile.Read(path)
	if err != nil {
		return statefile.State{}
	}
	return st
}

// tileRect finds the rectangle of window id in the tiled columns.
func tileRect(st statefile.State, id uint64) (image.Rectangle, bool) {
	for _, ws := range st.Workspaces {
		for _, c := range ws.Columns {
			for _, w := range c.Windows {
				if w.ID == id {
					return image.Rect(w.X, w.Y, w.X+w.Width, w.Y+w.Height), true
				}
			}
		}
	}
	return image.Rectangle{}, false
}

// greenShare is the fraction of pixels of r in the PNG at path that are
// clearly green; 0 when the image cannot be read yet.
func greenShare(path string, r image.Rectangle) float64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return 0
	}
	r = r.Intersect(img.Bounds())
	if r.Empty() {
		return 0
	}
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			if cg>>8 > 200 && cr>>8 < 60 && cb>>8 < 60 {
				n++
			}
		}
	}
	return float64(n) / float64(r.Dx()*r.Dy())
}

// magentaShare is the fraction of pixels of r in the PNG at path that are
// clearly magenta; 0 when the image cannot be read yet.
func magentaShare(path string, r image.Rectangle) float64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return 0
	}
	r = r.Intersect(img.Bounds())
	if r.Empty() {
		return 0
	}
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			if cr>>8 > 200 && cg>>8 < 60 && cb>>8 > 200 {
				n++
			}
		}
	}
	return float64(n) / float64(r.Dx()*r.Dy())
}

// TestXwaylandOverride: an override-redirect window (a menu) of an X11 client
// is drawn above the toplevel at its X position, is not a window in the
// state, leaves keyboard focus on the toplevel and vanishes when unmapped.
func TestXwaylandOverride(t *testing.T) {
	e := startXwayland(t)
	// Probe unmaps the menu 3 s after mapping it; the first checks run well
	// inside that window.
	p := startProbe(t, e.bin, e.display, "-title", "menu", "-override", "-override-at", "100,50",
		"-override-size", "120x80", "-override-for", "3s")
	until(t, "override mapped", func() bool { return strings.Contains(p.out.String(), "override mapped") })
	shot := filepath.Join(e.shots, "latest.png")
	box := image.Rect(100, 50, 220, 130) // output-local: one output at the X origin
	until(t, "magenta menu in latest.png", func() bool { return magentaShare(shot, box) >= 0.9 })

	var st statefile.State
	until(t, "focused X11Probe toplevel", func() bool {
		st = readState(e.statePath)
		return len(st.Windows) >= 1 && st.Window != nil
	})
	if len(st.Windows) != 1 || st.Windows[0].AppID != "X11Probe" {
		t.Fatalf("state windows = %+v, want only the X11Probe toplevel", st.Windows)
	}
	id := st.Windows[0].ID
	if st.Window.ID != id {
		t.Errorf("focused window = %+v, want the toplevel %d", st.Window, id)
	}
	until(t, "probe focus", func() bool { return strings.Contains(p.out.String(), "focus") })

	// The menu is above the toplevel: its box is magenta, not green.
	if g := greenShare(shot, box); g > 0.1 {
		t.Errorf("green share in menu box = %.2f, menu is not above the toplevel", g)
	}
	until(t, "override unmapped", func() bool { return strings.Contains(p.out.String(), "override unmapped") })
	until(t, "menu gone from latest.png", func() bool {
		return magentaShare(shot, box) == 0 && greenShare(shot, box) >= 0.9
	})
	if st := readState(e.statePath); st.Window == nil || st.Window.ID != id {
		t.Errorf("focus after the menu closed = %+v, want %d", st.Window, id)
	}
}

// TestXwaylandOverrideDismiss: the menu owner sees FocusOut on the toplevel
// when core focuses another window, which is how toolkits close their menus.
func TestXwaylandOverrideDismiss(t *testing.T) {
	e := startXwayland(t)
	p := startProbe(t, e.bin, e.display, "-title", "menu", "-override", "-override-dismiss-on-focusout")
	until(t, "override mapped", func() bool { return strings.Contains(p.out.String(), "override mapped") })
	until(t, "probe focus", func() bool { return strings.Contains(p.out.String(), "focus") })
	// A second X11 window takes the focus away from the menu owner.
	startProbe(t, e.bin, e.display, "-title", "other")
	until(t, "menu dismissed on FocusOut", func() bool { return strings.Contains(p.out.String(), "dismissed") })
	box := image.Rect(100, 50, 220, 130)
	until(t, "menu gone from latest.png", func() bool {
		return magentaShare(filepath.Join(e.shots, "latest.png"), box) == 0
	})
}

// TestXwaylandOverrideShape: a menu with an empty input shape is still drawn
// and still not a window.
func TestXwaylandOverrideShape(t *testing.T) {
	e := startXwayland(t)
	p := startProbe(t, e.bin, e.display, "-title", "menu", "-override", "-override-shape", "-override-type", "tooltip")
	until(t, "override mapped", func() bool { return strings.Contains(p.out.String(), "override mapped") })
	until(t, "magenta tooltip in latest.png", func() bool {
		return magentaShare(filepath.Join(e.shots, "latest.png"), image.Rect(100, 50, 220, 130)) >= 0.9
	})
	until(t, "only the toplevel in state", func() bool { return len(readState(e.statePath).Windows) == 1 })
}

// fullOutput is the share of the output screenshot at path that is green.
func fullOutput(path string) float64 {
	return greenShare(path, image.Rect(0, 0, xwaylandW, xwaylandH))
}

// windowOf returns the state window with the given pid.
func windowOf(st statefile.State, pid int) (statefile.Window, bool) {
	for _, w := range st.Windows {
		if w.PID == pid {
			return w, true
		}
	}
	return statefile.Window{}, false
}

// TestXwaylandFullscreen: a delayed fullscreen request fills the output,
// and the toggle bind leaves it.
func TestXwaylandFullscreen(t *testing.T) {
	// Gaps make a tile visibly smaller than the output.
	e := startXwaylandWith(t, [][2]int{{xwaylandW, xwaylandH}}, func(c *domain.Config) { c.Layout.Gaps = 20 })
	shot := filepath.Join(e.shots, "latest.png")
	corner := image.Rect(0, 0, 4, 4)
	p := startProbe(t, e.bin, e.display, "-fullscreen", "-fullscreen-delay", "2s")
	until(t, "tiled with a gap", func() bool {
		return strings.Contains(p.out.String(), "mapped ") && fullOutput(shot) > 0.3 && greenShare(shot, corner) == 0
	})
	until(t, "probe fullscreen at output size", func() bool {
		return strings.Contains(p.out.String(), "fullscreen 640x360")
	})
	until(t, "output all green", func() bool { return fullOutput(shot) >= 0.99 })

	// The toggle bind (cmd+shift+f) leaves fullscreen.
	if _, err := io.WriteString(e.script, "key Super+Shift+f\n"); err != nil {
		t.Fatal(err)
	}
	until(t, "probe left fullscreen", func() bool { return strings.Contains(p.out.String(), "fullscreen off ") })
	until(t, "gap back", func() bool { return greenShare(shot, corner) == 0 && fullOutput(shot) < 0.99 })
}

// TestXwaylandFullscreenStats watches an X11 game after leaving and re-entering
// fullscreen, then closes the client after showing the stats overlay.
func TestXwaylandFullscreenStats(t *testing.T) {
	e := startXwayland(t)
	p := startProbe(t, e.bin, e.display, "-fullscreen", "-fullscreen-delay", "1500ms", "-fullscreen-off", "1s")
	until(t, "fullscreen", func() bool { return strings.Contains(p.out.String(), "fullscreen 640x360") })
	until(t, "fullscreen off", func() bool { return strings.Contains(p.out.String(), "fullscreen off ") })
	if _, err := io.WriteString(e.script, "key Super+Shift+f\n"); err != nil {
		t.Fatal(err)
	}
	until(t, "fullscreen again", func() bool { return strings.Count(p.out.String(), "fullscreen 640x360") >= 2 })
	// Holding cmd activates WatchStats for the focused X11 window. The OSD is
	// excluded from screenshots; the subsequent close verifies the compositor
	// and XWM survived the stats request.
	if _, err := io.WriteString(e.script, "hold Super_L 1s\nkey Super+q\n"); err != nil {
		t.Fatal(err)
	}
	until(t, "disconnected X11 client removed", func() bool { return len(readState(e.statePath).Windows) == 0 })
	next := startProbe(t, e.bin, e.display, "-class", "AfterStats")
	until(t, "XWM still manages new windows", func() bool {
		return strings.Contains(next.out.String(), "mapped ") && len(readState(e.statePath).Windows) == 1
	})
}

// TestXwaylandFullscreenOff: the client leaves fullscreen itself.
func TestXwaylandFullscreenOff(t *testing.T) {
	e := startXwayland(t)
	p := startProbe(t, e.bin, e.display, "-fullscreen", "-fullscreen-delay", "1500ms", "-fullscreen-off", "1s")
	until(t, "fullscreen", func() bool { return strings.Contains(p.out.String(), "fullscreen 640x360") })
	until(t, "fullscreen off", func() bool { return strings.Contains(p.out.String(), "fullscreen off ") })
	until(t, "tiled", func() bool {
		st := readState(e.statePath)
		return len(st.Windows) == 1 && func() bool { _, ok := tileRect(st, st.Windows[0].ID); return ok }()
	})
}

// TestXwaylandInitialFullscreen: a fixed-size window that maps with
// _NET_WM_STATE_FULLSCREEN already set (a Proton game) opens fullscreen in a
// column of its own, focused, beside the tiled window already open. This is
// the state it maps with, rather than a later request.
func TestXwaylandInitialFullscreen(t *testing.T) {
	e := startXwaylandWith(t, [][2]int{{xwaylandW, xwaylandH}}, func(c *domain.Config) { c.Layout.Gaps = 20 })
	shot := filepath.Join(e.shots, "latest.png")
	first := startProbe(t, e.bin, e.display, "-class", "First", "-size", "200x100")
	until(t, "first tiled", func() bool {
		return strings.Contains(first.out.String(), "mapped ") && len(readState(e.statePath).Windows) == 1
	})
	p := startProbe(t, e.bin, e.display, "-class", "Game", "-initial-fullscreen", "-size", "640x360")
	until(t, "game mapped", func() bool { return strings.Contains(p.out.String(), "mapped ") })
	until(t, "game in its own column, fullscreen", func() bool {
		st := readState(e.statePath)
		for _, w := range st.Windows {
			if w.AppID == "Game" {
				return w.Xwayland && !w.Floating && w.Column == 2 && w.Row == 1
			}
		}
		return false
	})
	until(t, "output all green", func() bool { return fullOutput(shot) >= 0.99 })
	until(t, "game focused", func() bool { return strings.Contains(p.out.String(), "focus") })
}

// TestXwaylandImmediateFullscreen accepts a request immediately after mapping.
// Gaps distinguish a tiled window from an output-filling fullscreen window.
func TestXwaylandImmediateFullscreen(t *testing.T) {
	e := startXwaylandWith(t, [][2]int{{xwaylandW, xwaylandH}}, func(c *domain.Config) { c.Layout.Gaps = 20 })
	p := startProbe(t, e.bin, e.display, "-fullscreen")
	until(t, "published fullscreen state", func() bool {
		st := readState(e.statePath)
		return len(st.Windows) == 1 && st.Windows[0].Fullscreen
	})
	until(t, "probe fullscreen at output size", func() bool { return strings.Contains(p.out.String(), "fullscreen 640x360") })
	until(t, "output all green", func() bool { return fullOutput(filepath.Join(e.shots, "latest.png")) >= 0.99 })
}

// TestXwaylandKeyboardGrab: while a fullscreen, focused X11 client holds the
// keyboard grab the close bind does not fire; after it leaves fullscreen the
// binds work again.
//
//testpolicy:realtime bounded wait on real X11/Wayland sockets or child process I/O
func TestXwaylandKeyboardGrab(t *testing.T) {
	e := startXwayland(t)
	p := startProbe(t, e.bin, e.display, "-fullscreen", "-fullscreen-delay", "1500ms", "-fullscreen-off", "2500ms", "-keyboard-grab")
	until(t, "fullscreen", func() bool { return strings.Contains(p.out.String(), "fullscreen 640x360") })
	until(t, "grab", func() bool { return strings.Contains(p.out.String(), "grab") })
	until(t, "probe focus", func() bool { return strings.Contains(p.out.String(), "focus") })
	if _, err := io.WriteString(e.script, "key Super+q\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-p.waited:
		t.Fatalf("probe closed (%v) by Super+q despite the keyboard grab", err)
	case <-time.After(2 * time.Second):
	}

	// The probe leaves fullscreen itself; its grab stays, but the binds work again.
	until(t, "fullscreen off", func() bool { return strings.Contains(p.out.String(), "fullscreen off ") })
	if _, err := io.WriteString(e.script, "key Super+q\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-p.waited:
		if err != nil {
			t.Errorf("probe exited with %v, want 0", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Super+q did not close the client after it left fullscreen")
	}
}

// TestXwaylandKeyboardGrabWindowed: a grab on a windowed client does not lock
// the binds.
//
//testpolicy:realtime bounded wait on real X11/Wayland sockets or child process I/O
func TestXwaylandKeyboardGrabWindowed(t *testing.T) {
	e := startXwayland(t)
	p := startProbe(t, e.bin, e.display, "-keyboard-grab")
	until(t, "grab", func() bool { return strings.Contains(p.out.String(), "grab") })
	until(t, "probe focus", func() bool { return strings.Contains(p.out.String(), "focus") })
	if _, err := io.WriteString(e.script, "key Super+q\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-p.waited:
		if err != nil {
			t.Errorf("probe exited with %v, want 0", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Super+q did not close a windowed client that holds a keyboard grab")
	}
}

// TestXwaylandOutputHint: with two outputs a window created at the origin of
// the second one opens on it, and a fullscreen request fills that output.
func TestXwaylandOutputHint(t *testing.T) {
	sizes := [][2]int{{xwaylandW, xwaylandH}, {xwaylandW, xwaylandH}}
	e := startXwaylandWith(t, sizes, nil)
	p := startProbe(t, e.bin, e.display, "-size", "640x360", "-at", "640,0", "-fullscreen", "-fullscreen-delay", "1500ms")
	var w statefile.Window
	until(t, "window on HEADLESS-2", func() bool {
		var ok bool
		w, ok = windowOf(readState(e.statePath), p.cmd.Process.Pid)
		if ok && w.Output != "HEADLESS-2" {
			t.Logf("window output = %s", w.Output)
		}
		return ok && w.Output == "HEADLESS-2"
	})
	until(t, "probe fullscreen", func() bool { return strings.Contains(p.out.String(), "fullscreen 640x360") })
	until(t, "HEADLESS-2 all green", func() bool {
		return fullOutput(filepath.Join(e.shots, "HEADLESS-2", "latest.png")) >= 0.99
	})
	if g := fullOutput(filepath.Join(e.shots, "HEADLESS-1", "latest.png")); g > 0.1 {
		t.Errorf("HEADLESS-1 green share = %.2f, want empty", g)
	}
}

// TestXwaylandClassRule: [window-rule] matches the WM_CLASS of an X11 window
// the way it matches a Wayland app ID (steam_app_<id> from Proton).
func TestXwaylandClassRule(t *testing.T) {
	yes := true
	e := startXwaylandWith(t, [][2]int{{xwaylandW, xwaylandH}}, func(c *domain.Config) {
		c.Rules = append(c.Rules, domain.WindowRule{Name: "games", AppID: regexp.MustCompile(`^steam_app_\d+$`), Floating: &yes})
	})
	startProbe(t, e.bin, e.display, "-class", "steam_app_413150")
	until(t, "floating steam_app window", func() bool {
		st := readState(e.statePath)
		if len(st.Windows) == 1 {
			t.Logf("rule state: %+v", st.Windows[0])
		}
		return len(st.Windows) == 1 && st.Windows[0].AppID == "steam_app_413150" && st.Windows[0].Xwayland && st.Windows[0].Floating
	})
	startProbe(t, e.bin, e.display, "-class", "Other")
	until(t, "other class tiled", func() bool {
		for _, w := range readState(e.statePath).Windows {
			if w.AppID == "Other" {
				return w.Xwayland && !w.Floating
			}
		}
		return false
	})
}

// waylandEnv is the environment of a Wayland client of the headless
// compositor: its socket, not the user's session.
func waylandEnv() []string {
	return append(envWithout("DISPLAY", "WAYLAND_DISPLAY", "NEFERWL_STATE", "NEFERWL_SLOT"), "WAYLAND_DISPLAY=wayland-1")
}

// runProbe runs a one-shot x11probe mode and returns its stdout.
//
//testpolicy:realtime bounded wait on real X11/Wayland sockets or child process I/O
func runProbe(t *testing.T, e *xwaylandEnv, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), xwaylandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.bin, args...)
	cmd.Env = append(envWithout("DISPLAY", "WAYLAND_DISPLAY", "NEFERWL_STATE", "NEFERWL_SLOT"), "DISPLAY="+e.display)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("x11probe %v: %v (stdout %q)", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// needWlClipboard skips the test without wl-copy and wl-paste.
func needWlClipboard(t *testing.T) {
	t.Helper()
	for _, c := range []string{"wl-copy", "wl-paste"} {
		if _, err := exec.LookPath(c); err != nil {
			t.Skipf("%s not on PATH (wl-clipboard)", c)
		}
	}
}

// wlCopy starts wl-copy --foreground serving data on the headless
// compositor's seat and stops it when the test ends.
func wlCopy(t *testing.T, data string, args ...string) {
	t.Helper()
	cmd := exec.Command("wl-copy", append([]string{"--foreground", "--type", "text/plain;charset=utf-8"}, args...)...)
	cmd.Env = waylandEnv()
	cmd.Stdin = strings.NewReader(data)
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-waited
		if s := stderr.String(); s != "" {
			t.Logf("wl-copy stderr: %s", s)
		}
	})
}

// wlPaste returns what wl-paste prints, and whether it succeeded.
//
//testpolicy:realtime bounded wait on real X11/Wayland sockets or child process I/O
func wlPaste(args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), xwaylandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "wl-paste", append([]string{"--no-newline", "--type", "text/plain;charset=utf-8"}, args...)...)
	cmd.Env = waylandEnv()
	out, err := cmd.Output()
	return string(out), err == nil
}

// payload is n bytes of printable text that differ along the way, so a
// misplaced chunk is caught.
func payload(n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "line %d é\n", i)
	}
	return b.String()[:n]
}

// pasteLine is what x11probe -paste prints for want.
func pasteLine(want string) string {
	if len(want) <= 4096 {
		return fmt.Sprintf("paste %q", want)
	}
	return fmt.Sprintf("paste len=%d sha256=%x", len(want), sha256.Sum256([]byte(want)))
}

// TestXwaylandClipboard: the clipboard and the primary selection cross the
// X11/Wayland boundary in both directions, text only, small and INCR sized.
func TestXwaylandClipboard(t *testing.T) {
	needWlClipboard(t)
	e := startXwayland(t)
	startProbe(t, e.bin, e.display, "-title", "focus")
	until(t, "probe focused", func() bool { st := readState(e.statePath); return st.Window != nil })

	for _, sel := range []struct{ x, w string }{{"CLIPBOARD", ""}, {"PRIMARY", "--primary"}} {
		for _, size := range []int{4 << 10, 1 << 20} {
			name := fmt.Sprintf("%s/%dKiB", sel.x, size>>10)
			text := payload(size)
			t.Run("W2X/"+name, func(t *testing.T) {
				var wargs []string
				if sel.w != "" {
					wargs = append(wargs, sel.w)
				}
				wlCopy(t, text, wargs...)
				until(t, "x11probe pastes the Wayland copy", func() bool {
					return runProbe(t, e, "-paste", sel.x) == pasteLine(text)
				})
			})
			t.Run("X2W/"+name, func(t *testing.T) {
				file := filepath.Join(t.TempDir(), "text")
				if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
				p := startProbe(t, e.bin, e.display, "-copy", "@"+file, "-copy-sel", sel.x, "-copy-for", "30s")
				until(t, "copy owned", func() bool { return strings.Contains(p.out.String(), "copy owned") })
				var wargs []string
				if sel.w != "" {
					wargs = append(wargs, sel.w)
				}
				var got string
				until(t, "wl-paste returns the X copy", func() bool {
					var ok bool
					got, ok = wlPaste(wargs...)
					return ok && got == text
				})
				if got != text {
					t.Errorf("wl-paste differs: %d bytes", len(got))
				}
			})
		}
	}

	t.Run("clear", func(t *testing.T) {
		wlCopy(t, "to be cleared")
		until(t, "paste before clear", func() bool { return runProbe(t, e, "-paste", "CLIPBOARD") == `paste "to be cleared"` })
		cmd := exec.Command("wl-copy", "--clear")
		cmd.Env = waylandEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("wl-copy --clear: %v: %s", err, out)
		}
		until(t, "paste none after clear", func() bool { return runProbe(t, e, "-paste", "CLIPBOARD") == "paste none" })
	})
}

// cursorTheme installs a one-arrow Xcursor theme of the given size and
// selects it through the XCURSOR_* environment.
func cursorTheme(t *testing.T, size int) {
	t.Helper()
	dir := t.TempDir()
	themeCursor(t, dir, "left_ptr", size, 1)
	t.Setenv("XCURSOR_PATH", dir)
	t.Setenv("XCURSOR_THEME", "theme")
	t.Setenv("XCURSOR_SIZE", strconv.Itoa(size))
}

// TestXwaylandXSettings: the XSETTINGS manager publishes the cursor theme,
// the cursor size and a fixed scale 1 / 96 dpi.
func TestXwaylandXSettings(t *testing.T) {
	cursorTheme(t, 32)
	e := startXwayland(t)
	want := "xsettings serial=1 Xft/DPI=98304 Gdk/WindowScalingFactor=1 Gdk/UnscaledDPI=98304 Gtk/CursorThemeName=theme Gtk/CursorThemeSize=32"
	var got string
	until(t, "XSETTINGS owner", func() bool { got = runProbe(t, e, "-xsettings"); return got != "xsettings none" })
	if got != want {
		t.Errorf("-xsettings = %q, want %q", got, want)
	}
}

// TestXwaylandXSettingsScale2AnnouncesScale1: X11 sees logical sizes, so the
// DPI stays 96 even on a scale-2 output.
func TestXwaylandXSettingsScale2AnnouncesScale1(t *testing.T) {
	cursorTheme(t, 24)
	e := startXwaylandWith(t, [][2]int{{xwaylandW, xwaylandH}}, func(c *domain.Config) {
		c.Outputs = append(c.Outputs, domain.OutputConfig{Name: "HEADLESS-1", Scale: 2, ScaleOnly: true})
	})
	var got string
	until(t, "XSETTINGS owner", func() bool { got = runProbe(t, e, "-xsettings"); return got != "xsettings none" })
	if !strings.Contains(got, "Xft/DPI=98304") || !strings.Contains(got, "Gdk/WindowScalingFactor=1") {
		t.Errorf("-xsettings = %q, want Xft/DPI=98304 and Gdk/WindowScalingFactor=1", got)
	}
	if !strings.Contains(got, "Gtk/CursorThemeSize=24") {
		t.Errorf("-xsettings = %q, want Gtk/CursorThemeSize=24", got)
	}
}

// TestXwaylandXSettingsOff: xwayland.xsettings = off leaves no manager.
func TestXwaylandXSettingsOff(t *testing.T) {
	e := startXwaylandWith(t, [][2]int{{xwaylandW, xwaylandH}}, func(c *domain.Config) { c.Xwayland.XSettings = false })
	// A mapped window proves the XWM is up before the absence is checked.
	p := startProbe(t, e.bin, e.display)
	until(t, "probe mapped", func() bool { return strings.Contains(p.out.String(), "mapped ") })
	if got := runProbe(t, e, "-xsettings"); got != "xsettings none" {
		t.Errorf("-xsettings = %q, want none", got)
	}
}

// TestXwaylandRootCursor: the X background shows the themed arrow, at the
// theme size.
func TestXwaylandRootCursor(t *testing.T) {
	cursorTheme(t, 32)
	e := startXwayland(t)
	until(t, "XWM up", func() bool { return runProbe(t, e, "-xsettings") != "xsettings none" })
	if got := runProbe(t, e, "-root-cursor"); got != "cursor 32x32" {
		t.Errorf("-root-cursor = %q, want cursor 32x32", got)
	}
}

// themeCursor writes a one-image Xcursor file of side size under
// dir/theme/cursors/name, every pixel set to px.
func themeCursor(t *testing.T, dir, name string, size int, px byte) {
	t.Helper()
	le := binary.LittleEndian
	out := le.AppendUint32([]byte("Xcur"), 16)
	out = le.AppendUint32(out, 0x10000)
	out = le.AppendUint32(out, 1)
	for _, v := range []int{0xfffd0002, size, 28, 36, 0xfffd0002, size, 1, size, size, 0, 0, 0} {
		out = le.AppendUint32(out, uint32(v))
	}
	for range size * size * 4 {
		out = append(out, px)
	}
	path := filepath.Join(dir, "theme", "cursors", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func publicationChanges(t *testing.T) chan struct{} {
	var changes any
	var ok bool
	name := t.Name()
	for {
		xwaylandPublications.Range(func(k, v any) bool {
			if k.(*testing.T).Name() == name {
				changes, ok = v, true
				return false
			}
			return true
		})
		if ok {
			break
		}
		i := strings.LastIndex(name, "/")
		if i < 0 {
			break
		}
		name = name[:i]
	}
	if !ok {
		t.Fatal("missing publication observer")
	}
	return changes.(chan struct{})
}

// TestXwaylandServerProbe: on the server backend, with one virtual output
// and no fixed head, an X11 client maps on the virtual output, takes the
// focus and closes with the close-window bind.
//
//testpolicy:realtime bounded wait on real X11/Wayland sockets or child process I/O
func TestXwaylandServerProbe(t *testing.T) {
	e := startXwaylandBackend(t, domain.BackendServer, nil, func(c *domain.Config) {
		c.Outputs = append(c.Outputs, domain.OutputConfig{Name: "tv", Virtual: true, Mode: "640x360"})
	})
	p := startProbe(t, e.bin, e.display, "-title", "server")
	var st statefile.State
	until(t, "X11Probe visible on tv", func() bool {
		st = readState(e.statePath)
		return st.Output == "tv" && len(st.Windows) == 1 && st.Windows[0].AppID == "X11Probe" && st.Windows[0].Visible && st.Windows[0].Output == "tv"
	})
	if !st.Windows[0].Xwayland {
		t.Errorf("X11 window not marked Xwayland: %+v", st.Windows[0])
	}
	until(t, "probe focus", func() bool { return strings.Contains(p.out.String(), "focus") })
	if _, err := io.WriteString(e.script, "key Super+q\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-p.waited:
		if err != nil {
			t.Errorf("probe exited with %v, want 0", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("probe did not exit within 2s of Super+q")
	}
}
