// Package libinput reads keyboards and pointers through libinput's udev backend.
package libinput

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/bnema/neferwl/internal/adapters/xkb"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// ErrEmergencyQuit is returned when Ctrl+Alt+Backspace is pressed.
var ErrEmergencyQuit = errors.New("emergency quit key")

const (
	evDeviceAdded   = 1
	evDeviceRemoved = 2
	evKeyboardKey   = 300
	evPointerMotion = 400
	evPointerAbs    = 401
	evPointerButton = 402
	evScrollWheel   = 404
	evScrollFinger  = 405
	evScrollCont    = 406
	evSwipeBegin    = 800
	evSwipeUpdate   = 801
	evSwipeEnd      = 802
)

var (
	loadOnce sync.Once
	loadErr  error

	udevNew         func() uintptr
	udevUnref       func(uintptr) uintptr
	createContext   func(iface unsafe.Pointer, data uintptr, udev uintptr) uintptr
	assignSeat      func(li uintptr, seat string) int32
	unref           func(li uintptr) uintptr
	getFD           func(li uintptr) int32
	dispatch        func(li uintptr) int32
	getEvent        func(li uintptr) uintptr
	eventType       func(ev uintptr) int32
	eventDestroy    func(ev uintptr)
	eventDevice     func(ev uintptr) uintptr
	deviceName      func(dev uintptr) string
	suspend         func(li uintptr)
	resume          func(li uintptr) int32
	keyboardEvent   func(ev uintptr) uintptr
	keyboardKey     func(kev uintptr) uint32
	keyboardState   func(kev uintptr) int32
	keyboardUsec    func(kev uintptr) uint64
	pointerEvent    func(ev uintptr) uintptr
	pointerDX       func(pev uintptr) float64
	pointerDY       func(pev uintptr) float64
	pointerRawDX    func(pev uintptr) float64
	pointerRawDY    func(pev uintptr) float64
	pointerUsec     func(pev uintptr) uint64
	pointerAbsX     func(pev uintptr, width uint32) float64
	pointerAbsY     func(pev uintptr, height uint32) float64
	pointerButton   func(pev uintptr) uint32
	pointerBtnState func(pev uintptr) int32
	pointerHasAxis  func(pev uintptr, axis uint32) int32
	scrollValue     func(pev uintptr, axis uint32) float64
	scrollV120      func(pev uintptr, axis uint32) float64
	gestureEvent    func(ev uintptr) uintptr
	gestureFingers  func(gev uintptr) int32
	gestureCanceled func(gev uintptr) int32
	gestureDX       func(gev uintptr) float64
	gestureDY       func(gev uintptr) float64
	gestureUsec     func(gev uintptr) uint64
	deviceRef       func(dev uintptr) uintptr
	deviceUnref     func(dev uintptr) uintptr
	hasNatural      func(dev uintptr) int32
	setNatural      func(dev uintptr, on int32) int32
	tapFingers      func(dev uintptr) int32
	setTap          func(dev uintptr, on int32) int32
	setTapMap       func(dev uintptr, m int32) int32
	iface           [2]uintptr
	active          ports.Seat
)

func load() error {
	loadOnce.Do(func() {
		udev, err := purego.Dlopen("libudev.so.1", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			loadErr = fmt.Errorf("open libudev.so.1: %w", err)
			return
		}
		lib, err := purego.Dlopen("libinput.so.10", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			loadErr = fmt.Errorf("open libinput.so.10: %w", err)
			return
		}
		defer func() {
			if r := recover(); r != nil {
				loadErr = fmt.Errorf("libinput: %v", r)
			}
		}()
		purego.RegisterLibFunc(&udevNew, udev, "udev_new")
		purego.RegisterLibFunc(&udevUnref, udev, "udev_unref")
		reg := func(fn any, name string) { purego.RegisterLibFunc(fn, lib, "libinput_"+name) }
		reg(&createContext, "udev_create_context")
		reg(&assignSeat, "udev_assign_seat")
		reg(&unref, "unref")
		reg(&getFD, "get_fd")
		reg(&dispatch, "dispatch")
		reg(&getEvent, "get_event")
		reg(&eventType, "event_get_type")
		reg(&eventDestroy, "event_destroy")
		reg(&eventDevice, "event_get_device")
		reg(&deviceName, "device_get_name")
		reg(&suspend, "suspend")
		reg(&resume, "resume")
		reg(&keyboardEvent, "event_get_keyboard_event")
		reg(&keyboardKey, "event_keyboard_get_key")
		reg(&keyboardState, "event_keyboard_get_key_state")
		reg(&keyboardUsec, "event_keyboard_get_time_usec")
		reg(&pointerEvent, "event_get_pointer_event")
		reg(&pointerDX, "event_pointer_get_dx")
		reg(&pointerDY, "event_pointer_get_dy")
		reg(&pointerRawDX, "event_pointer_get_dx_unaccelerated")
		reg(&pointerRawDY, "event_pointer_get_dy_unaccelerated")
		reg(&pointerUsec, "event_pointer_get_time_usec")
		reg(&pointerAbsX, "event_pointer_get_absolute_x_transformed")
		reg(&pointerAbsY, "event_pointer_get_absolute_y_transformed")
		reg(&pointerButton, "event_pointer_get_button")
		reg(&pointerBtnState, "event_pointer_get_button_state")
		reg(&pointerHasAxis, "event_pointer_has_axis")
		reg(&scrollValue, "event_pointer_get_scroll_value")
		reg(&scrollV120, "event_pointer_get_scroll_value_v120")
		reg(&gestureEvent, "event_get_gesture_event")
		reg(&gestureFingers, "event_gesture_get_finger_count")
		reg(&gestureCanceled, "event_gesture_get_cancelled")
		reg(&gestureDX, "event_gesture_get_dx_unaccelerated")
		reg(&gestureDY, "event_gesture_get_dy_unaccelerated")
		reg(&gestureUsec, "event_gesture_get_time_usec")
		reg(&deviceRef, "device_ref")
		reg(&deviceUnref, "device_unref")
		reg(&hasNatural, "device_config_scroll_has_natural_scroll")
		reg(&setNatural, "device_config_scroll_set_natural_scroll_enabled")
		reg(&tapFingers, "device_config_tap_get_finger_count")
		reg(&setTap, "device_config_tap_set_enabled")
		reg(&setTapMap, "device_config_tap_set_button_map")
		iface[0] = purego.NewCallback(func(path *byte, _, _ uintptr) uintptr {
			fd, err := active.OpenDevice(goString(path))
			if err != nil {
				return uintptr(uint32(^uint32(unix.EACCES) + 1)) // -EACCES as C int
			}
			return uintptr(fd)
		})
		iface[1] = purego.NewCallback(func(fd, _ uintptr) { active.CloseDevice(int(int32(fd))) })
	})
	return loadErr
}

func goString(p *byte) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(p, n))
}

// Options configures the input adapter.
type Options struct {
	Seat     ports.Seat
	SeatName string
	Keymap   *xkb.Keymap
	// Keymaps replaces Keymap live; Run takes ownership and closes the old one.
	Keymaps <-chan *xkb.Keymap
	// Layout is the initial output layout; Layouts replaces it live.
	Layout  ports.Layout
	Layouts <-chan ports.Layout
	// Constraints holds the pointer lock or confinement of the focused
	// window, in global logical coordinates.
	Constraints <-chan ports.PointerConstraint
	// Touchpad is the initial touchpad config; Touchpads replaces it live.
	Touchpad  ports.TouchpadConfig
	Touchpads <-chan ports.TouchpadConfig
	Active    <-chan bool
	// MoveCursor, when set, places the hardware cursor as soon as motion is
	// read, before core sees the event: the output under the pointer and
	// the physical position on it.
	MoveCursor func(output string, x, y float64)
	Log        zerowrap.Logger
	// LogMotion logs every pointer motion (--debug=input-motion); motions
	// are otherwise only counted in the input stats.
	LogMotion bool
}

// statsEvery is how often Run logs input throughput.
const statsEvery = 10 * time.Second

// Run owns libinput and the keymap, converting device events into input events.
func Run(ctx context.Context, opts Options, input chan<- ports.InputEvent) error {
	defer func() { opts.Keymap.Close() }()
	if err := load(); err != nil {
		return err
	}
	active = opts.Seat
	udev := udevNew()
	if udev == 0 {
		return errors.New("udev_new failed")
	}
	defer udevUnref(udev)
	li := createContext(unsafe.Pointer(&iface), 0, udev)
	if li == 0 {
		return errors.New("libinput_udev_create_context failed")
	}
	defer unref(li)
	name := opts.SeatName
	if name == "" {
		name = "seat0"
	}
	if assignSeat(li, name) != 0 {
		return fmt.Errorf("libinput_udev_assign_seat %s failed", name)
	}
	p := newPointer(opts.Layout)
	p.moved(opts.MoveCursor)
	in := &inputState{touchpad: opts.Touchpad, devices: map[uintptr]bool{}, swipes: swipes{}}
	defer in.release()
	fd := getFD(li)
	fwd := newForwarder(opts.Log)
	fwdCtx, stopFwd := context.WithCancel(ctx)
	var fwdDone sync.WaitGroup
	fwdDone.Go(func() { fwd.run(fwdCtx, input) })
	defer func() { stopFwd(); fwdDone.Wait() }()
	lastStats := time.Now()
	for ctx.Err() == nil {
		if time.Since(lastStats) >= statsEvery {
			lastStats = time.Now()
			if s := fwd.take(); s.Motions > 0 || s.Sent > 0 {
				opts.Log.Info().Int("motions", s.Motions).Int("coalesced", s.Coalesced).Int("sent", s.Sent).Dur("max_block_ms", s.MaxBlock).Msg("input stats")
			}
		}
		select {
		case on := <-opts.Active:
			if on {
				resume(li)
			} else {
				suspend(li)
			}
			opts.Log.Info().Bool("active", on).Msg("input")
		case km := <-opts.Keymaps:
			opts.Keymap.Close()
			opts.Keymap = km
			opts.Log.Info().Msg("keymap replaced")
		case l := <-opts.Layouts:
			p.setLayout(l)
			p.moved(opts.MoveCursor)
		case c := <-opts.Constraints:
			p.constrain(c)
			p.moved(opts.MoveCursor)
		case t := <-opts.Touchpads:
			in.touchpad = t
			for dev := range in.devices {
				in.configure(dev, opts.Log)
			}
			opts.Log.Info().Bool("natural_scroll", t.NaturalScroll).Bool("tap", t.Tap).Msg("touchpad reloaded")
		default:
		}
		fds := []unix.PollFd{{Fd: fd, Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 100)
		if err != nil && !errors.Is(err, unix.EINTR) {
			return fmt.Errorf("libinput poll: %w", err)
		}
		if n <= 0 {
			continue
		}
		if dispatch(li) != 0 {
			opts.Log.Warn().Msg("libinput_dispatch failed")
		}
		for ev := getEvent(li); ev != 0; ev = getEvent(li) {
			out, err := translate(ev, opts, p, in)
			eventDestroy(ev)
			if err != nil {
				return err
			}
			if out != nil {
				fwd.push(out)
			}
		}
	}
	return nil
}

// inputState is the device state Run owns: touchpad config, the devices
// it configures (referenced until removed) and the swipe in progress on
// each touchpad.
type inputState struct {
	touchpad ports.TouchpadConfig
	devices  map[uintptr]bool
	swipes   swipes
}

// configurable reports whether the touchpad config applies to dev: it has
// natural scroll or tapping.
func configurable(dev uintptr) bool { return hasNatural(dev) != 0 || tapFingers(dev) > 0 }

// tapButtonMap is LIBINPUT_CONFIG_TAP_MAP_LRM: one finger taps left, two
// right, three middle.
const tapButtonMap = 0

func (s *inputState) configure(dev uintptr, log zerowrap.Logger) {
	if hasNatural(dev) != 0 && setNatural(dev, flag(s.touchpad.NaturalScroll)) != 0 {
		log.Warn().Str("device", deviceName(dev)).Msg("natural scroll rejected")
	}
	if tapFingers(dev) > 0 {
		if setTap(dev, flag(s.touchpad.Tap)) != 0 {
			log.Warn().Str("device", deviceName(dev)).Msg("tap to click rejected")
		}
		if setTapMap(dev, tapButtonMap) != 0 {
			log.Warn().Str("device", deviceName(dev)).Msg("tap button map rejected")
		}
	}
}

func flag(on bool) int32 {
	if on {
		return 1
	}
	return 0
}

func (s *inputState) release() {
	for dev := range s.devices {
		deviceUnref(dev)
	}
	clear(s.devices)
}

// swipeFingers is the finger count of the swipes neferwl handles; other
// counts are left alone.
const swipeFingers = 3

// swipes streams the three-finger swipe of each touchpad to core, which
// follows the fingers.
type swipes map[uintptr]bool

func (s swipes) begin(dev uintptr, fingers int, at time.Duration) ports.InputEvent {
	if fingers != swipeFingers {
		return nil
	}
	s[dev] = true
	return ports.SwipeBegin{Time: at}
}

func (s swipes) update(dev uintptr, dx, dy float64, at time.Duration) ports.InputEvent {
	if !s[dev] {
		return nil
	}
	return ports.SwipeUpdate{DX: dx, DY: dy, Time: at}
}

func (s swipes) end(dev uintptr, cancelled bool, at time.Duration) ports.InputEvent {
	if !s[dev] {
		return nil
	}
	delete(s, dev)
	return ports.SwipeEnd{Cancelled: cancelled, Time: at}
}

// usec is a libinput timestamp as a duration.
func usec(v uint64) time.Duration { return time.Duration(v) * time.Microsecond }

// msec is a libinput timestamp in wayland's millisecond clock.
func msec(v uint64) uint32 { return uint32(v / 1000) }

// translate converts one libinput event. Timestamps are the device's
// (CLOCK_MONOTONIC), so clients measure speeds (kinetic scrolling) on
// when events happened, not when they were read.
func translate(ev uintptr, opts Options, p *pointer, in *inputState) (ports.InputEvent, error) {
	log := opts.Log
	switch eventType(ev) {
	case evDeviceAdded:
		dev := eventDevice(ev)
		log.Info().Str("device", deviceName(dev)).Msg("input device added")
		if configurable(dev) && !in.devices[dev] {
			in.devices[deviceRef(dev)] = true
			in.configure(dev, log)
		}
	case evDeviceRemoved:
		dev := eventDevice(ev)
		log.Info().Str("device", deviceName(dev)).Msg("input device removed")
		if in.devices[dev] {
			delete(in.devices, dev)
			deviceUnref(dev)
		}
		// Core waits for the end of a swipe the device began.
		return in.swipes.end(dev, true, 0), nil
	case evSwipeBegin:
		ge := gestureEvent(ev)
		return in.swipes.begin(eventDevice(ev), int(gestureFingers(ge)), usec(gestureUsec(ge))), nil
	case evSwipeUpdate:
		ge := gestureEvent(ev)
		return in.swipes.update(eventDevice(ev), gestureDX(ge), gestureDY(ge), usec(gestureUsec(ge))), nil
	case evSwipeEnd:
		ge := gestureEvent(ev)
		return in.swipes.end(eventDevice(ev), gestureCanceled(ge) != 0, usec(gestureUsec(ge))), nil
	case evKeyboardKey:
		k := keyboardEvent(ev)
		code, pressed := keyboardKey(k), keyboardState(k) == 1
		ke := opts.Keymap.Key(code, pressed, msec(keyboardUsec(k)))
		log.Debug().Uint32("code", code).Str("keysym", ke.Keysym).Bool("pressed", pressed).Uint8("mods", uint8(ke.Mods)).Msg("key")
		switch action, vt := hotkey(ke); action {
		case hotkeyQuit:
			log.Warn().Str("reason", "emergency-key").Msg("quit")
			return nil, ErrEmergencyQuit
		case hotkeyVT:
			opts.Seat.SwitchVT(vt)
			return nil, nil
		}
		return ke, nil
	case evPointerMotion:
		pe := pointerEvent(ev)
		m := p.move(pointerDX(pe), pointerDY(pe))
		m.UnaccelDX, m.UnaccelDY = pointerRawDX(pe), pointerRawDY(pe)
		m.TimeUsec = pointerUsec(pe)
		m.TimeMsec = msec(m.TimeUsec)
		p.moved(opts.MoveCursor)
		if opts.LogMotion {
			log.Debug().Float64("x", m.X).Float64("y", m.Y).Msg("pointer")
		}
		return m, nil
	case evPointerAbs:
		// Absolute devices (tablets, VMs) map to the whole layout.
		pe := pointerEvent(ev)
		now := msec(pointerUsec(pe))
		b := p.bounds()
		if p.constraint.Mode == ports.ConstraintLock {
			return ports.PointerMotion{X: p.x, Y: p.y, TimeMsec: now}, nil
		}
		x, y := p.set(float64(b.X)+pointerAbsX(pe, uint32(b.W)), float64(b.Y)+pointerAbsY(pe, uint32(b.H)))
		p.moved(opts.MoveCursor)
		return ports.PointerMotion{X: x, Y: y, TimeMsec: now}, nil
	case evPointerButton:
		pe := pointerEvent(ev)
		b, pressed := pointerButton(pe), pointerBtnState(pe) == 1
		log.Debug().Uint32("button", b).Bool("pressed", pressed).Msg("button")
		return ports.PointerButton{Button: b, Pressed: pressed, TimeMsec: msec(pointerUsec(pe))}, nil
	case evScrollWheel, evScrollFinger, evScrollCont:
		pe := pointerEvent(ev)
		// The three scroll events follow each other as the sources do.
		source := ports.AxisSource(eventType(ev) - evScrollWheel)
		a := ports.PointerAxis{Source: source, TimeMsec: msec(pointerUsec(pe))}
		// libinput axis 0 is vertical, 1 horizontal, as in wl_pointer.
		for i, s := range []*ports.ScrollAxis{&a.Vertical, &a.Horizontal} {
			if pointerHasAxis(pe, uint32(i)) == 0 {
				continue
			}
			s.Set, s.Value = true, scrollValue(pe, uint32(i))
			if source == ports.AxisWheel {
				s.V120 = int32(scrollV120(pe, uint32(i)))
			} else {
				s.Stop = s.Value == 0
			}
		}
		log.Debug().Float64("v", a.Vertical.Value).Float64("h", a.Horizontal.Value).Int32("v120", a.Vertical.V120).Int32("hv120", a.Horizontal.V120).Uint8("source", uint8(a.Source)).Msg("scroll")
		return a, nil
	}
	return nil, nil
}

type hotkeyAction int

const (
	hotkeyNone hotkeyAction = iota
	hotkeyQuit
	hotkeyVT
)

// hotkey detects compositor-reserved keys on press: Ctrl+Alt+Backspace and Ctrl+Alt+F1..F12.
func hotkey(ke ports.KeyEvent) (hotkeyAction, int) {
	if !ke.Pressed {
		return hotkeyNone, 0
	}
	if vt, ok := strings.CutPrefix(ke.Keysym, "XF86Switch_VT_"); ok {
		var n int
		if _, err := fmt.Sscan(vt, &n); err == nil && n >= 1 && n <= 12 {
			return hotkeyVT, n
		}
	}
	if ke.Mods&(ports.ModCtrl|ports.ModAlt) != ports.ModCtrl|ports.ModAlt {
		return hotkeyNone, 0
	}
	switch {
	case ke.Keysym == "BackSpace" || ke.Keycode == 14:
		return hotkeyQuit, 0
	case ke.Keycode >= 59 && ke.Keycode <= 68: // F1..F10
		return hotkeyVT, int(ke.Keycode) - 58
	case ke.Keycode == 87 || ke.Keycode == 88: // F11, F12
		return hotkeyVT, int(ke.Keycode) - 76
	}
	return hotkeyNone, 0
}

// pointer tracks the cursor in global logical coordinates. Relative motion
// is divided by the scale of the output under the pointer, so it moves the
// same number of physical pixels on every output.
type pointer struct {
	x, y       float64
	layout     ports.Layout
	touched    bool // false until the user moves it: it follows the primary output
	constraint ports.PointerConstraint
}

// newPointer starts at the centre of the primary output, else the first.
func newPointer(l ports.Layout) *pointer {
	p := &pointer{}
	p.setLayout(l)
	return p
}

// setLayout keeps the pointer where it is, or clamps it onto an output.
// Until it is first moved it stays centred on the primary output, which
// may be plugged in after the others.
func (p *pointer) setLayout(l ports.Layout) {
	p.layout = l
	if !p.touched && len(l) > 0 {
		o := l[0]
		for _, v := range l {
			if v.Primary {
				o = v
			}
		}
		p.x, p.y = float64(o.X)+float64(o.Width)/2, float64(o.Y)+float64(o.Height)/2
		return
	}
	p.x, p.y = l.Clamp(p.x, p.y, p.x, p.y)
}

func (p *pointer) scale() float64 {
	if o, ok := p.layout.At(p.x, p.y); ok && o.Scale > 0 {
		return o.Scale
	}
	return 1
}

// move applies a relative motion and returns it with the logical deltas.
// A locked pointer stays still; a confined one stays in its rectangle.
func (p *pointer) move(dx, dy float64) ports.PointerMotion {
	s := p.scale()
	dx, dy = dx/s, dy/s
	if p.constraint.Mode != ports.ConstraintLock {
		p.set(p.x+dx, p.y+dy)
	}
	return ports.PointerMotion{X: p.x, Y: p.y, DX: dx, DY: dy}
}

func (p *pointer) set(x, y float64) (float64, float64) {
	p.touched = true
	p.x, p.y = p.constraint.Clamp(p.layout.Clamp(p.x, p.y, x, y))
	return p.x, p.y
}

// constrain applies a new constraint. Entering or leaving a lock resyncs
// the pointer to core's cursor, which stood still meanwhile; a confined
// pointer moves inside.
func (p *pointer) constrain(c ports.PointerConstraint) {
	if c.Mode == ports.ConstraintLock || p.constraint.Mode == ports.ConstraintLock {
		p.x, p.y = p.layout.Clamp(c.X, c.Y, c.X, c.Y)
	}
	p.constraint = c
	p.x, p.y = c.Clamp(p.x, p.y)
}

// bounds is the rectangle holding every output.
func (p *pointer) bounds() ports.Rect {
	var r ports.Rect
	for _, o := range p.layout {
		r.W = max(r.W, o.X+o.Width)
		r.H = max(r.H, o.Y+o.Height)
	}
	return r
}

// moved reports the physical position on the output under the pointer.
func (p *pointer) moved(move func(output string, x, y float64)) {
	if o, ok := p.layout.At(p.x, p.y); ok && move != nil {
		move(o.Info.Name, (p.x-float64(o.X))*o.Scale, (p.y-float64(o.Y))*o.Scale)
	}
}
