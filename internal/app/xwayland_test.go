//go:build xwayland

// End-to-end test of the built-in XWM: a real Xwayland and a real X11 client
// (testdata/x11probe) run against a headless NeferWL. It needs Xwayland on
// PATH and a GPU, so it is excluded from `go test ./...` and CI. Run it with
// `make e2e-xwayland`.
package app

import (
	"context"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/neferwl/internal/adapters/config"
	"github.com/bnema/neferwl/internal/adapters/statefile"
	"github.com/bnema/neferwl/internal/ports"
)

const (
	xwaylandTimeout      = 10 * time.Second
	xwaylandW, xwaylandH = 640, 360
)

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
	logs := &lockedBuffer{}
	ctx, cancel := context.WithCancel(zerowrap.WithCtx(context.Background(), zerowrap.New(zerowrap.Config{Level: "info", Format: "json", Output: logs})))
	cfg := config.Defaults()
	cfg.Xwayland = ports.XwaylandConfig{Backend: ports.XwaylandBuiltin, Binary: "Xwayland"}
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Backend: "headless", NoTerminal: true, Config: cfg, Script: script,
			ScreenshotDir: shots, Sizes: [][2]int{{xwaylandW, xwaylandH}},
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
func startProbe(t *testing.T, bin, display string, args ...string) *probe {
	t.Helper()
	p := &probe{out: &lockedBuffer{}, waited: make(chan error, 1)}
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

// until polls cond every 20 ms and fails the test after xwaylandTimeout.
func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(xwaylandTimeout); !cond(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
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
