package screencast

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bnema/zerobus"
	"github.com/stretchr/testify/mock"
)

// Screenshot: nefercap on the focused output; the reply carries the file.
func TestScreenshotRunsNefercapOnFocusedOutput(t *testing.T) {
	rt := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", rt)
	h, c := ready(t, harnessOpts{})
	got := make(chan []string, 1)
	h.run.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, args []string) error {
		got <- slices.Clone(args)
		return nil
	}).Once()
	name, m := callErr(c, nameScreenshot, pathScreenshot, ifaceScreenshot, "Screenshot", "bbs", func(e *zerobus.Encoder) {
		e.Bool(true)
		e.Bool(true)
		e.Str("ignored")
	})
	if name != "" {
		t.Fatal(name)
	}
	r := m.Body()
	ok, file := r.Bool(), strings.Clone(r.Str())
	args := <-got
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
	if name, _ := callErr(c, nameScreenshot, pathScreenshot, ifaceScreenshot, "ScreenshotArea", "iiiibs", area(70, 4, 100, 10)); name != "" {
		t.Fatal(name)
	}
	args := <-got
	if args[1] != "-output" || args[2] != "HEADLESS-2" || args[3] != "-region" || args[4] != "6,4,26x10" {
		t.Fatalf("args %v", args)
	}
	if name, _ := callErr(c, nameScreenshot, pathScreenshot, ifaceScreenshot, "ScreenshotArea", "iiiibs", area(500, 4, 10, 10)); name != errInvalidArgs {
		t.Fatalf("outside: %q", name)
	}
	if name, _ := callErr(c, nameScreenshot, pathScreenshot, ifaceScreenshot, "ScreenshotArea", "iiiibs", area(0, 0, 0, 10)); name != errInvalidArgs {
		t.Fatalf("empty: %q", name)
	}
}

// InteractiveScreenshot: nefercap shot, answered with a file:// uri. A second
// shot while one runs is LimitsExceeded; a failure is Failed and the busy
// flag clears.
func TestInteractiveScreenshotBusyAndFailure(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	h, c := ready(t, harnessOpts{})
	release := make(chan error)
	got := make(chan []string, 2)
	h.run.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, args []string) error {
		got <- slices.Clone(args)
		return <-release
	}).Twice()

	c.NewCall(nameScreenshot, pathScreenshot, ifaceScreenshot, "InteractiveScreenshot", "")
	first, err := c.Send()
	if err != nil {
		t.Fatal(err)
	}
	args := <-got
	if len(args) != 3 || args[0] != "shot" || args[1] != "-file" {
		t.Fatalf("args %v", args)
	}
	other := h.client()
	if name, _ := callErr(other, nameScreenshot, pathScreenshot, ifaceScreenshot, "InteractiveScreenshot", "", nil); name != errLimits {
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
	<-got
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
		if name, _ := callErr(c, nameScreenshot, pathScreenshot, ifaceScreenshot, member, "", nil); name != errNotSupported && name != errInvalidArgs {
			t.Errorf("%s: %q", member, name)
		}
	}
}
