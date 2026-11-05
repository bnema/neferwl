package control

import (
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bnema/neferwl/internal/adapters/xkb"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

const controlDeviceID domain.InputDeviceID = 1

// injector translates commands on its owner's goroutine. It borrows km; the
// caller owns its lifetime and announces Device once before delivering input.
// Coordinates are global logical coordinates; layout clamping belongs to core.
type injector struct {
	km       *xkb.Keymap
	security ports.SessionSecurity
}

func newInjector(km *xkb.Keymap, security ports.SessionSecurity) *injector {
	return &injector{km: km, security: security}
}

func injectTime() time.Duration {
	var ts unix.Timespec
	_ = unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
	return time.Duration(ts.Nano())
}

func (i *injector) snapshot() domain.SecurityState {
	if i.security != nil {
		return i.security.Snapshot()
	}
	return domain.SecurityState{}
}

func (i *injector) stamp(ev ports.InputEvent, state domain.SecurityState) ports.InputEvent {
	if i.security != nil {
		return ports.SecurityInput{State: state, Event: ev}
	}
	return ev
}

// Device returns the announcement to send once for this synthetic producer.
func (i *injector) Device() ports.InputEvent {
	return i.stamp(ports.InputDeviceAdded{ID: controlDeviceID, Name: "control"}, i.snapshot())
}

func (i *injector) keyEdge(events []ports.InputEvent, code uint32, down bool) ([]ports.InputEvent, error) {
	state := i.snapshot() // Must precede KeySecure's epoch-aware translation.
	var key ports.KeyEvent
	if i.security != nil {
		var deliver bool
		var err error
		key, deliver, err = i.km.KeySecure(code, down, injectTime(), state)
		if err != nil {
			return nil, err
		}
		if !deliver {
			return events, nil
		}
	} else {
		key = i.km.Key(code, down, injectTime())
	}
	key.Device = controlDeviceID
	return append(events, i.stamp(key, state)), nil
}

func injectModifier(name string) uint32 {
	switch name {
	case "Super":
		return 125
	case "Shift":
		return 42
	case "Ctrl":
		return 29
	case "Alt":
		return 56
	}
	return 0
}

// combo resolves all keys before any keymap state is changed.
func (i *injector) combo(combo string) ([]uint32, error) {
	parts := strings.Split(combo, "+")
	var codes []uint32
	for _, name := range parts[:len(parts)-1] {
		code := injectModifier(name)
		if code == 0 {
			return nil, errors.New("unknown modifier")
		}
		codes = append(codes, code)
	}
	name := parts[len(parts)-1]
	// Modifier-only presses use the same evdev identities as script press/release.
	if code := injectModifier(name); code != 0 {
		return append(codes, code), nil
	}
	code, shift, ok := i.km.KeycodeFor(name)
	if !ok {
		return nil, errors.New("unknown key")
	}
	if shift && !slices.Contains(codes, uint32(42)) && !slices.Contains(codes, uint32(54)) {
		codes = append(codes, 42)
	}
	return append(codes, code), nil
}

func (i *injector) edges(codes []uint32, down bool, events []ports.InputEvent) ([]ports.InputEvent, error) {
	for n := range codes {
		at := n
		if !down {
			at = len(codes) - 1 - n
		}
		var err error
		events, err = i.keyEdge(events, codes[at], down)
		if err != nil {
			return nil, err
		}
	}
	return events, nil
}

func (i *injector) Key(combo string) ([]ports.InputEvent, error) {
	codes, err := i.combo(combo)
	if err != nil {
		return nil, err
	}
	events, err := i.edges(codes, true, nil)
	if err != nil {
		return nil, err
	}
	return i.edges(codes, false, events)
}

func (i *injector) Press(combo string) ([]ports.InputEvent, error) {
	codes, err := i.combo(combo)
	if err != nil {
		return nil, err
	}
	return i.edges(codes, true, nil)
}

func (i *injector) Release(combo string) ([]ports.InputEvent, error) {
	codes, err := i.combo(combo)
	if err != nil {
		return nil, err
	}
	return i.edges(codes, false, nil)
}

var injectPunctuation = map[rune]string{' ': "space", '\n': "Return", '.': "period", ',': "comma", '-': "minus", '_': "underscore", '/': "slash", ':': "colon", ';': "semicolon", '!': "exclam", '?': "question", '\'': "apostrophe", '"': "quotedbl", '(': "parenleft", ')': "parenright", '=': "equal", '+': "plus", '\\': "backslash", '@': "at", '#': "numbersign", '$': "dollar", '%': "percent", '&': "ampersand", '*': "asterisk", '[': "bracketleft", ']': "bracketright", '{': "braceleft", '}': "braceright", '<': "less", '>': "greater", '|': "bar", '`': "grave", '~': "asciitilde", '^': "asciicircum"}

func (i *injector) Type(text string) ([]ports.InputEvent, error) {
	var strokes [][]uint32
	for _, r := range text {
		name := injectPunctuation[r]
		if name == "" && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			name = string(r)
		}
		if name == "" {
			return nil, errors.New("unknown rune")
		}
		codes, err := i.combo(name)
		if err != nil {
			return nil, err
		}
		strokes = append(strokes, codes)
	}
	var events []ports.InputEvent
	for _, codes := range strokes {
		var err error
		events, err = i.edges(codes, true, events)
		if err != nil {
			return nil, err
		}
		events, err = i.edges(codes, false, events)
		if err != nil {
			return nil, err
		}
	}
	return events, nil
}

func (i *injector) Pointer(cmd string, args []string) ([]ports.InputEvent, error) {
	now := injectTime()
	if cmd == "move" {
		if len(args) != 2 {
			return nil, errors.New("move requires x y")
		}
		x, ex := strconv.ParseFloat(args[0], 64)
		y, ey := strconv.ParseFloat(args[1], 64)
		if ex != nil || ey != nil || math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
			return nil, errors.New("invalid move coordinates")
		}
		return []ports.InputEvent{i.stamp(ports.PointerMotion{Device: controlDeviceID, X: x, Y: y, Time: now}, i.snapshot())}, nil
	}
	if cmd != "click" && cmd != "down" && cmd != "up" {
		return nil, errors.New("unknown pointer command")
	}
	if len(args) > 1 || len(args) == 0 && cmd != "click" {
		return nil, errors.New("invalid button arguments")
	}
	name := "left"
	if len(args) == 1 {
		name = args[0]
	}
	var button uint32
	switch name {
	case "left":
		button = 0x110
	case "right":
		button = 0x111
	case "middle":
		button = 0x112
	default:
		return nil, errors.New("unknown button")
	}
	var events []ports.InputEvent
	if cmd != "up" {
		events = append(events, i.stamp(ports.PointerButton{Device: controlDeviceID, Button: button, Pressed: true, Time: now}, i.snapshot()))
	}
	if cmd != "down" {
		events = append(events, i.stamp(ports.PointerButton{Device: controlDeviceID, Button: button, Time: now}, i.snapshot()))
	}
	return events, nil
}

func (i *injector) Swipe(fingers int, dir string) ([]ports.InputEvent, error) {
	if fingers != 3 && fingers != 4 {
		return nil, errors.New("swipe requires three or four fingers")
	}
	var dx, dy float64
	switch dir {
	case "up":
		dy = -40
	case "down":
		dy = 40
	case "left":
		dx = -40
	case "right":
		dx = 40
	default:
		return nil, errors.New("invalid swipe direction")
	}
	at := injectTime()
	events := []ports.InputEvent{i.stamp(ports.SwipeBegin{Device: controlDeviceID, Fingers: fingers, Time: at}, i.snapshot())}
	for range 10 {
		at += 8 * time.Millisecond
		events = append(events, i.stamp(ports.SwipeUpdate{Device: controlDeviceID, DX: dx, DY: dy, Time: at}, i.snapshot()))
	}
	return append(events, i.stamp(ports.SwipeEnd{Device: controlDeviceID, Time: at}, i.snapshot())), nil
}
