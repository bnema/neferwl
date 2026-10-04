//go:build colorcheck

// On-demand colour check: a real Wayland client (examples/testpattern) shows
// known colours on a headless NeferWL and the screenshots are compared with
// what those colours must become. It needs a GPU and, for the PQ client, /dev/udmabuf,
// so it is excluded from `go test ./...`, `make check` and CI. Run it with
// `make color-check`.
package app

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/examples/testpattern/pattern"
	"github.com/bnema/neferwl/internal/adapters/config"
)

const (
	colorW, colorH = 640, 360
	// sampleRadius gives the 5×5 box sampled at each patch centre.
	sampleRadius = 2
	// cursorRadius is the half size of the box around the output centre that
	// headless screenshots draw the cursor into.
	cursorRadius = 48
	// pqTolerance is the per-channel PQ signal tolerance, the one of
	// TestHDRWindowedComposition; it covers the fp16 round trip.
	pqTolerance = 0.012
	// nitTolerance is the alternative HDR tolerance, in absolute luminance,
	// for the channels expected below nitFloor.
	//
	// Near black PQ is very steep, and the compositor's fp16 linear
	// intermediate leaves a small residue there: with BT.2020 primaries
	// outside BT.709 the BT.709 intermediate has a slightly negative or
	// rounded component, and the BT.2020 green primary patch measures
	// R=0.0313 PQ instead of 0 on an RX 9070 XT. That is about 0.02 nit,
	// invisible and not a colour bug, so a channel expected below nitFloor
	// also passes when the decoded luminance differs by no more than this.
	nitTolerance = 0.03
	// nitFloor is the expected luminance (nits) under which the luminance
	// fallback applies; brighter channels must meet pqTolerance.
	nitFloor = 0.05
	// pqNamedTF is the wp_color_manager_v1 transfer function of an HDR output
	// (st2084_pq).
	pqNamedTF = 11
	// colorTimeout bounds each wait for the client and for a matching frame.
	colorTimeout = 5 * time.Second
)

// TestColorCheckSDR: an sRGB wl_shm client on an SDR output is read back
// byte for byte (±1 per channel).
func TestColorCheckSDR(t *testing.T) {
	dir := t.TempDir()
	socket := startColorCompositor(t, Options{ScreenshotDir: dir})
	client := startPatternClient(t, socket, "sdr")
	patches := pattern.Patches("sdr", colorW, colorH)
	assertNoCursorOverlap(t, patches)

	pollImage(t, filepath.Join(dir, "latest.png"), client.ready, "", func(img image.Image) []string {
		return comparePatches(img, patches, func(p pattern.Patch, got [3]float64) string {
			for c := range 3 {
				if math.Abs(got[c]*255-float64(p.SRGB[c])) > 1 {
					return fmt.Sprintf("got %v, want sRGB %v ±1", bytes3(got), p.SRGB)
				}
			}
			return ""
		})
	})
}

// TestColorCheckHDR checks a virtual HDR output through the raw PQ dump
// (latest-pq.png), with an sRGB client and with a PQ client.
//
// Virtual HDR must be on: output-tf other than 11 means it fell back to SDR,
// which is a failure, not a skip. Since headless exports its targets without
// a display modifier list, it works on any GPU that exports XR30, and the dev
// box does. The target requires a GPU: a Vulkan error fails the test. Only a
// missing /dev/udmabuf or linear XR30 import (PQ client) skips.
//
// Expected path: PQ client content is decoded to fp16 linear BT.709 (/203,
// compose_hdr.frag), then re-encoded (×203, BT.709→BT.2020, PQ, hdr.frag);
// sRGB content maps to PQ(M·linear·203). There is no dithering, and the
// cursor and the capture indicator are not in the exported target.
func TestColorCheckHDR(t *testing.T) {
	dir := t.TempDir()
	socket := startColorCompositor(t, Options{ScreenshotDir: dir, HeadlessHDR: true, ScreenshotRaw: true})
	pq := filepath.Join(dir, "latest-pq.png")

	t.Run("sdr-client", func(t *testing.T) {
		client := startPatternClient(t, socket, "sdr")
		patches := pattern.Patches("sdr", colorW, colorH)
		assertNoCursorOverlap(t, patches)
		pollImage(t, pq, client.ready, "no raw PQ dump (virtual HDR off?)", func(img image.Image) []string {
			return comparePatches(img, patches, func(p pattern.Patch, got [3]float64) string {
				var want [3]float64
				var lin [3]float64
				for c := range 3 {
					lin[c] = pattern.SRGBToLinear(float64(p.SRGB[c]) / 255)
				}
				for r := range 3 {
					for c := range 3 {
						want[r] += pattern.BT709ToBT2020[r][c] * lin[c]
					}
					want[r] = pattern.PQEncode(want[r] * pattern.ReferenceWhite)
				}
				return comparePQ(got, want)
			})
		})
	})

	t.Run("pq-client", func(t *testing.T) {
		if _, err := os.Stat("/dev/udmabuf"); err != nil {
			t.Skipf("no /dev/udmabuf: %v", err)
		}
		client := startPatternClient(t, socket, "hdr")
		if client.tf != pqNamedTF {
			t.Fatalf("output-tf %d, want %d: virtual HDR fell back to SDR", client.tf, pqNamedTF)
		}
		patches := pattern.Patches("hdr", colorW, colorH)
		assertNoCursorOverlap(t, patches)
		pollImage(t, pq, client.ready, "no raw PQ dump (virtual HDR off?)", func(img image.Image) []string {
			return comparePatches(img, patches, func(p pattern.Patch, got [3]float64) string {
				var want [3]float64
				for c := range 3 {
					want[c] = pattern.PQEncode(p.Nits[c])
				}
				return comparePQ(got, want)
			})
		})
		// Tone mapping itself is unit-tested: only check the 8-bit
		// screenshot is not black at the 1000-nit patch.
		pollImage(t, filepath.Join(dir, "latest.png"), client.ready, "", func(img image.Image) []string {
			c := patches[5].Centre()
			r, g, b, _ := img.At(c.X, c.Y).RGBA()
			if r|g|b == 0 {
				return []string{fmt.Sprintf("latest.png is black at the 1000-nit patch (%d,%d)", c.X, c.Y)}
			}
			return nil
		})
	})
}

// comparePQ compares one patch, per channel: it passes within pqTolerance of
// signal or, for a channel expected below nitFloor, within nitTolerance of
// luminance (see nitTolerance).
func comparePQ(got, want [3]float64) string {
	for c := range 3 {
		if math.Abs(got[c]-want[c]) <= pqTolerance {
			continue
		}
		if wantNits := pattern.PQDecode(want[c]); wantNits < nitFloor && math.Abs(pattern.PQDecode(got[c])-wantNits) <= nitTolerance {
			continue
		}
		return fmt.Sprintf("got PQ %.4f %.4f %.4f (%.3f %.3f %.3f nit), want %.4f %.4f %.4f (%.3f %.3f %.3f nit); ±%v PQ, or ±%v nit under %v nit",
			got[0], got[1], got[2], pattern.PQDecode(got[0]), pattern.PQDecode(got[1]), pattern.PQDecode(got[2]),
			want[0], want[1], want[2], pattern.PQDecode(want[0]), pattern.PQDecode(want[1]), pattern.PQDecode(want[2]),
			pqTolerance, nitTolerance, nitFloor)
	}
	return ""
}

func bytes3(v [3]float64) [3]int {
	return [3]int{int(math.Round(v[0] * 255)), int(math.Round(v[1] * 255)), int(math.Round(v[2] * 255))}
}

// comparePatches samples the 5×5 box at the centre of every patch; check gets
// each pixel as unit-range channels and returns a mismatch description or "".
// It returns one line per mismatching patch, or one for a wrong image size.
func comparePatches(img image.Image, patches []pattern.Patch, check func(p pattern.Patch, got [3]float64) string) []string {
	if b := img.Bounds(); b.Dx() != colorW || b.Dy() != colorH {
		return []string{fmt.Sprintf("image is %v, want %dx%d", b.Size(), colorW, colorH)}
	}
	var bad []string
	for i, p := range patches {
		c := p.Centre()
	box:
		for y := c.Y - sampleRadius; y <= c.Y+sampleRadius; y++ {
			for x := c.X - sampleRadius; x <= c.X+sampleRadius; x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				got := [3]float64{float64(r) / 65535, float64(g) / 65535, float64(b) / 65535}
				if msg := check(p, got); msg != "" {
					bad = append(bad, fmt.Sprintf("patch %d at (%d,%d): %s", i, x, y, msg))
					break box
				}
			}
		}
	}
	return bad
}

// assertNoCursorOverlap checks no sampling box touches the cursor, which
// headless screenshots draw at the output centre.
func assertNoCursorOverlap(t *testing.T, patches []pattern.Patch) {
	t.Helper()
	cursor := image.Rect(colorW/2-cursorRadius, colorH/2-cursorRadius, colorW/2+cursorRadius, colorH/2+cursorRadius)
	for i, p := range patches {
		c := p.Centre()
		box := image.Rect(c.X-sampleRadius, c.Y-sampleRadius, c.X+sampleRadius+1, c.Y+sampleRadius+1)
		if box.Overlaps(cursor) {
			t.Fatalf("patch %d sampling box %v intersects the cursor box %v", i, box, cursor)
		}
	}
}

// pollImage decodes path every 50 ms, up to colorTimeout, until check returns
// no mismatch. Only files modified after notBefore count, so a frame of a
// previous client is never judged (files are replaced atomically). On timeout
// it reports every mismatch of the last attempt; missing, when not empty,
// replaces the error of a file that does not exist.
func pollImage(t *testing.T, path string, notBefore time.Time, missing string, check func(image.Image) []string) {
	t.Helper()
	var last []string
	for deadline := time.Now().Add(colorTimeout); ; time.Sleep(50 * time.Millisecond) {
		last = nil
		if fi, err := os.Stat(path); err != nil {
			last = []string{err.Error()}
			if missing != "" && os.IsNotExist(err) {
				last = []string{missing}
			}
		} else if !fi.ModTime().After(notBefore) {
			last = []string{fmt.Sprintf("%s not rewritten since the client started", filepath.Base(path))}
		} else if f, err := os.Open(path); err != nil {
			last = []string{err.Error()}
		} else {
			img, err := png.Decode(f)
			_ = f.Close()
			if err != nil {
				last = []string{err.Error()}
			} else if last = check(img); len(last) == 0 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s does not match after %v:\n  %s", filepath.Base(path), colorTimeout, strings.Join(last, "\n  "))
		}
	}
}

// startColorCompositor runs a headless NeferWL with one 640×360 output in
// temporary runtime and state directories and returns its socket path. A Run
// error, Vulkan included, fails the test: the target requires a GPU.
func startColorCompositor(t *testing.T, opts Options) string {
	t.Helper()
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	opts.Backend = "headless"
	opts.NoXwayland = true
	opts.NoTerminal = true
	opts.Config = config.Defaults()
	opts.Sizes = [][2]int{{colorW, colorH}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, opts) }()
	stopped := false // Run's result was already received
	t.Cleanup(func() {
		cancel()
		if stopped {
			return
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Run did not stop")
		}
	})
	socket := filepath.Join(runtime, "wayland-1")
	for deadline := time.Now().Add(colorTimeout); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(socket); err == nil {
			return socket
		}
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("neferwl exited: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("no wayland socket")
		}
	}
}

// patternClient is a running examples/testpattern.
type patternClient struct {
	ready time.Time // just before the client was started: older files are not judged
	tf    int       // "output-tf" of the compositor's output (hdr mode)
}

// startPatternClient builds and starts examples/testpattern with -fullscreen
// and returns once it printed "ready 640 360". It skips the test when the
// client reports that linear XR30 is unsupported, and stops the client when
// the test (or subtest) ends.
func startPatternClient(t *testing.T, socket, mode string) *patternClient {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "testpattern")
	build := exec.Command("go", "build", "-o", bin, "../../examples/testpattern")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build testpattern: %v: %s", err, out)
	}
	cmd := exec.Command(bin, "-mode", mode, "-fullscreen")
	cmd.Env = append(os.Environ(), "WAYLAND_DISPLAY="+filepath.Base(socket))
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	// Taken before the start so that no frame of this client predates it.
	// Frames of its windowed state (before fullscreen) are newer, so they
	// are judged: they just fail the check and are retried.
	started := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	exited := false
	t.Cleanup(func() {
		if !exited {
			_ = cmd.Process.Kill()
			<-waited
		}
	})

	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(10 * time.Millisecond) { // includes the 1.2 s fullscreen delay
		log := stderr.String()
		if strings.Contains(log, fmt.Sprintf("ready %d %d\n", colorW, colorH)) {
			c := &patternClient{ready: started}
			for _, l := range strings.Split(log, "\n") {
				if v, ok := strings.CutPrefix(l, "output-tf "); ok {
					tf, err := strconv.Atoi(v)
					if err != nil {
						t.Fatalf("bad %q", l)
					}
					c.tf = tf
				}
			}
			return c
		}
		select {
		case err := <-waited:
			exited = true
			log = stderr.String()
			if strings.Contains(log, "linear XR30 unsupported") {
				t.Skipf("testpattern: %s", strings.TrimSpace(log))
			}
			t.Fatalf("testpattern exited before ready (%v): %s", err, strings.TrimSpace(log))
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("testpattern not ready: %s", strings.TrimSpace(log))
		}
	}
}
