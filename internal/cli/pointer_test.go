package cli

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseButton(t *testing.T) {
	for in, want := range map[string]uint32{
		"left": 0x110, "RIGHT": 0x111, "middle": 0x112, "side": 0x113, "extra": 0x114,
		"forward": 0x115, "back": 0x116, "BTN_LEFT": 0x110, "btn_back": 0x116,
		"272": 0x110, "0x111": 0x111,
	} {
		if got, ok := parseButton(in); !ok || got != want {
			t.Errorf("%q = %#x %v, want %#x", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "up", "1", "0x200", "-1", "BTN_JOYSTICK"} {
		if _, ok := parseButton(in); ok {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestParsePointerActions(t *testing.T) {
	got, err := parsePointerActions(strings.Fields("abs 0.5 1 move -10 2.5 press right release right click click middle hold hold side 2s hold 1s drag 30 0 drag 5 5 back scroll down scroll left 3 click"))
	if err != nil {
		t.Fatal(err)
	}
	want := []pointerAction{
		{kind: "abs", x: 0.5, y: 1, button: btnLeft},
		{kind: "move", x: -10, y: 2.5, button: btnLeft},
		{kind: "press", button: 0x111},
		{kind: "release", button: 0x111},
		{kind: "click", button: btnLeft},
		{kind: "click", button: 0x112},
		{kind: "hold", button: btnLeft},
		{kind: "hold", button: 0x113, duration: 2 * time.Second},
		{kind: "hold", button: btnLeft, duration: time.Second},
		{kind: "drag", x: 30, button: btnLeft},
		{kind: "drag", x: 5, y: 5, button: 0x116},
		{kind: "scroll", steps: 1, button: btnLeft},
		{kind: "scroll", steps: -3, axis: 1, button: btnLeft},
		{kind: "click", button: btnLeft},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestParsePointerActionErrors(t *testing.T) {
	for _, in := range []string{
		"", "jump", "abs 0.5", "abs 2 0", "abs -0.1 0", "abs x y", "move 1", "move NaN 0",
		"move Inf 0", "drag 1", "scroll", "scroll sideways", "scroll up 0", "hold -1s",
	} {
		_, err := parsePointerActions(strings.Fields(in))
		var u usageError
		if !errors.As(err, &u) {
			t.Errorf("%q: %v, want a usage error", in, err)
		}
	}
}

// Bad flags and actions exit 2 with the usage line, before connecting.
func TestPointerCommandUsage(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	for _, args := range [][]string{
		{"pointer"},
		{"pointer", "fly"},
		{"pointer", "--drag-steps", "0", "click"},
		{"pointer", "--click-duration", "-1s", "click"},
		{"pointer", "--bogus", "click"},
	} {
		code, _, stderr := execute(t, args...)
		if code != 2 || !strings.Contains(stderr, "usage: neferwl pointer") {
			t.Errorf("%v: code %d stderr %q", args, code, stderr)
		}
	}
	code, stdout, _ := execute(t, "pointer", "-h")
	if code != 0 || !strings.Contains(stdout, "drag DX DY [BTN]") || !strings.Contains(stdout, "-drag-step-delay") {
		t.Fatalf("help %d %q", code, stdout)
	}
	if code, _, stderr := execute(t, "pointer", "click"); code != 1 || !strings.Contains(stderr, "WAYLAND_DISPLAY") {
		t.Fatalf("no display: %d %q", code, stderr)
	}
}
