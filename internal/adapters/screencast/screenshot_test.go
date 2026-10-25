package screencast

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bnema/zerobus"
	"github.com/stretchr/testify/mock"
)

// writeShot is what nefercap does: it writes the PNG named by -file.
func writeShot(t *testing.T, args []string) {
	t.Helper()
	i := slices.Index(args, "-file")
	if i < 0 || i+1 >= len(args) {
		t.Errorf("no -file in %v", args)
		return
	}
	if err := os.WriteFile(args[i+1], []byte("\x89PNG"), 0o600); err != nil {
		t.Error(err)
	}
}

// Screenshot: nefercap on the focused output; the reply carries the file.
func TestScreenshotRunsNefercapOnFocusedOutput(t *testing.T) {
	rt := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", rt)
	h, c := ready(t, harnessOpts{})
	got := make(chan []string, 1)
	h.run.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, args []string) error {
		got <- slices.Clone(args)
		writeShot(t, args)
		return nil
	}).Once()
	name, m := callErr(t, c, nameScreenshot, pathScreenshot, ifaceScreenshot, "Screenshot", "bbs", func(e *zerobus.Encoder) {
		e.Bool(true)
		e.Bool(true)
		e.Str("ignored")
	})
	if name != "" {
		t.Fatal(name)
	}
	r := m.Body()
	ok, file := r.Bool(), strings.Clone(r.Str())
	args := receive(t, got)
	if !ok || !strings.HasPrefix(file, filepath.Join(rt, "neferwl", "screenshot-")) || !strings.HasSuffix(file, ".png") {
		t.Fatalf("reply %v %q", ok, file)
	}
	want := []string{"screenshot", "-output", "HEADLESS-1", "-file", file}
	if !slices.Equal(args, want) {
		t.Fatalf("args %v, want %v", args, want)
	}
	if fi, err := os.Stat(filepath.Join(rt, "neferwl")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("directory %v %v", fi, err)
	}
}

// ScreenshotArea: the output containing the corner, with a clipped,
// output-local region; outside every output is InvalidArgs.
func TestScreenshotAreaMapsToRegion(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	h, c := ready(t, harnessOpts{})
	got := make(chan []string, 1)
	h.run.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, args []string) error {
		got <- slices.Clone(args)
		writeShot(t, args)
		return nil
	}).Once()
	area := func(x, y, w, hh int32) func(*zerobus.Encoder) {
		return func(e *zerobus.Encoder) {
			e.Int32(x)
			e.Int32(y)
			e.Int32(w)
			e.Int32(hh)
			e.Bool(false)
			e.Str("f")
		}
	}
	// HEADLESS-2 sits at x=64, 32x32: the area starts at (70,4), wider than the rest of the output.
	if name, _ := callErr(t, c, nameScreenshot, pathScreenshot, ifaceScreenshot, "ScreenshotArea", "iiiibs", area(70, 4, 100, 10)); name != "" {
		t.Fatal(name)
	}
	args := receive(t, got)
	if args[1] != "-output" || args[2] != "HEADLESS-2" || args[3] != "-region" || args[4] != "6,4,26x10" {
		t.Fatalf("args %v", args)
	}
	if name, _ := callErr(t, c, nameScreenshot, pathScreenshot, ifaceScreenshot, "ScreenshotArea", "iiiibs", area(500, 4, 10, 10)); name != errInvalidArgs {
		t.Fatalf("outside: %q", name)
	}
	if name, _ := callErr(t, c, nameScreenshot, pathScreenshot, ifaceScreenshot, "ScreenshotArea", "iiiibs", area(0, 0, 0, 10)); name != errInvalidArgs {
		t.Fatalf("empty: %q", name)
	}
}

// InteractiveScreenshot: nefercap shot, answered with a file:// uri. A second
// shot while one runs is LimitsExceeded; a failure is Failed and the busy
// flag clears.
func TestInteractiveScreenshotBusyAndFailure(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	h, c := ready(t, harnessOpts{})
	checkReads := boundIO(t, "interactive screenshot replies", func() { _ = c.Close() })
	defer checkReads()
	release := make(chan error, 2)
	got := make(chan []string, 2)
	h.run.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, args []string) error {
		got <- slices.Clone(args)
		var err error
		select {
		case err = <-release:
		case <-time.After(5 * time.Second):
			return context.DeadlineExceeded
		}
		if err == nil {
			writeShot(t, args)
		}
		return err
	}).Twice()

	c.NewCall(nameScreenshot, pathScreenshot, ifaceScreenshot, "InteractiveScreenshot", "")
	first, err := c.Send()
	if err != nil {
		t.Fatal(err)
	}
	args := receive(t, got)
	if len(args) != 3 || args[0] != "shot" || args[1] != "-file" {
		t.Fatalf("args %v", args)
	}
	other := h.client()
	if name, _ := callErr(t, other, nameScreenshot, pathScreenshot, ifaceScreenshot, "InteractiveScreenshot", "", nil); name != errLimits {
		t.Fatalf("second shot: %q", name)
	}
	release <- nil
	m, err := c.ReadMessage()
	if err != nil || m.ReplySerial != first {
		t.Fatalf("reply %v %v", m, err)
	}
	r := m.Body()
	if ok, uri := r.Bool(), r.Str(); !ok || uri != "file://"+args[2] {
		t.Fatalf("reply %v %q", ok, uri)
	}

	// nefercap fails (or is missing): Failed, and the next shot is allowed.
	c.NewCall(nameScreenshot, pathScreenshot, ifaceScreenshot, "InteractiveScreenshot", "")
	if _, err := c.Send(); err != nil {
		t.Fatal(err)
	}
	receive(t, got)
	release <- os.ErrNotExist
	m, err = c.ReadMessage()
	if err != nil || m.ErrorName != errFailed {
		t.Fatalf("failure reply %+v %v", m, err)
	}
}

// What the dialog offers beyond the supported calls is NotSupported.
func TestScreenshotUnsupportedMethods(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	_, c := ready(t, harnessOpts{})
	for _, member := range []string{"SelectArea", "ScreenshotWindow", "PickColor", "FlashArea"} {
		if name, _ := callErr(t, c, nameScreenshot, pathScreenshot, ifaceScreenshot, member, "", nil); name != errNotSupported && name != errInvalidArgs {
			t.Errorf("%s: %q", member, name)
		}
	}
}

// nefercap that exits cleanly without leaving a regular, non-empty file is
// not a success: the file is missing, empty, or a symlink.
func TestScreenshotWithoutUsableFileIsNotSuccess(t *testing.T) {
	rt := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", rt)
	target := filepath.Join(rt, "real.png")
	if err := os.WriteFile(target, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, mk := range map[string]func(path string) error{
		"missing": func(string) error { return nil },
		"empty":   func(path string) error { return os.WriteFile(path, nil, 0o600) },
		"symlink": func(path string) error { return os.Symlink(target, path) },
		"dir":     func(path string) error { return os.Mkdir(path, 0o700) },
	} {
		t.Run(name, func(t *testing.T) {
			h, c := ready(t, harnessOpts{})
			h.run.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, args []string) error {
				if err := mk(args[slices.Index(args, "-file")+1]); err != nil {
					t.Error(err)
				}
				return nil
			}).Once()
			for _, member := range []string{"Screenshot", "InteractiveScreenshot"} {
				if member == "InteractiveScreenshot" {
					h.run.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, args []string) error {
						if err := mk(args[slices.Index(args, "-file")+1]); err != nil {
							t.Error(err)
						}
						return nil
					}).Once()
				}
				sig := ""
				body := func(*zerobus.Encoder) {}
				if member == "Screenshot" {
					sig = "bbs"
					body = func(e *zerobus.Encoder) { e.Bool(false); e.Bool(false); e.Str("x") }
				}
				errName, m := callErr(t, c, nameScreenshot, pathScreenshot, ifaceScreenshot, member, sig, body)
				if errName != "" {
					t.Fatalf("%s: %s", member, errName)
				}
				r := m.Body()
				if ok, file := r.Bool(), r.Str(); ok || file != "" {
					t.Fatalf("%s: success %v %q without a file", member, ok, file)
				}
			}
		})
	}
}

// A result that belongs to another job completes nothing, and a job that
// the attempt's end cancels is joined before its state is reset.
func TestStaleScreenshotResultIsDropped(t *testing.T) {
	p := newProducer(t)
	run := newMockrunner(t)
	p.s.runner = run
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.s.ctx = ctx
	started := make(chan struct{})
	var seen error
	run.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(func(c context.Context, _ []string) error {
		close(started)
		<-c.Done()
		seen = c.Err()
		return c.Err()
	}).Once()
	p.bus.EXPECT().NewError(mock.Anything, errFailed, "s").RunAndReturn(
		func(*zerobus.Message, string, string) *zerobus.Encoder { return &zerobus.Encoder{} }).Once()

	p.s.shotSeq++
	p.s.shot = shotState{busy: true, id: p.s.shotSeq, path: filepath.Join(t.TempDir(), "x.png")}
	p.s.runShot([]string{"shot"})
	receive(t, started)
	// A result of some earlier job is in the transport: it is not this job's.
	p.s.onShotDone(&shotResult{ready: true, id: p.s.shotSeq - 1, made: true})
	if !p.s.shot.busy {
		t.Fatal("a stale result completed the job in flight")
	}
	// The attempt ends: the job is cancelled and joined before the reset.
	p.s.stopShot()
	if seen == nil {
		t.Fatal("stopShot returned before the job saw its cancellation")
	}
	if p.s.shot.busy || p.s.shotRes.ready {
		t.Fatal("job state survives the attempt")
	}
	// Its own late result (not possible after the join, but harmless) and a
	// new job with a new ID: the first job's ID completes nothing.
	p.s.shotSeq++
	p.s.shot = shotState{busy: true, id: p.s.shotSeq}
	p.s.onShotDone(&shotResult{ready: true, id: p.s.shotSeq - 1, made: true})
	if !p.s.shot.busy {
		t.Fatal("a result of the cancelled job completed the next request")
	}
}
