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

	udevNew             func() uintptr
	udevUnref           func(uintptr) uintptr
	createContext       func(iface unsafe.Pointer, data uintptr, udev uintptr) uintptr
	assignSeat          func(li uintptr, seat string) int32
	unref               func(li uintptr) uintptr
	getFD               func(li uintptr) int32
	dispatch            func(li uintptr) int32
	getEvent            func(li uintptr) uintptr
	eventType           func(ev uintptr) int32
	eventDestroy        func(ev uintptr)
	eventDevice         func(ev uintptr) uintptr
	deviceName          func(dev uintptr) string
	suspend             func(li uintptr)
	resume              func(li uintptr) int32
	keyboardEvent       func(ev uintptr) uintptr
	keyboardKey         func(kev uintptr) uint32
	keyboardState       func(kev uintptr) int32
	keyboardUsec        func(kev uintptr) uint64
	pointerEvent        func(ev uintptr) uintptr
	pointerDX           func(pev uintptr) float64
	pointerDY           func(pev uintptr) float64
	pointerRawDX        func(pev uintptr) float64
	pointerRawDY        func(pev uintptr) float64
	pointerUsec         func(pev uintptr) uint64
	pointerAbsX         func(pev uintptr, width uint32) float64
	pointerAbsY         func(pev uintptr, height uint32) float64
	pointerButton       func(pev uintptr) uint32
	pointerBtnState     func(pev uintptr) int32
	pointerHasAxis      func(pev uintptr, axis uint32) int32
	scrollValue         func(pev uintptr, axis uint32) float64
	scrollV120          func(pev uintptr, axis uint32) float64
	gestureEvent        func(ev uintptr) uintptr
	gestureFingers      func(gev uintptr) int32
	gestureCanceled     func(gev uintptr) int32
	gestureDX           func(gev uintptr) float64
	gestureDY           func(gev uintptr) float64
	gestureUsec         func(gev uintptr) uint64
	deviceRef           func(dev uintptr) uintptr
	deviceUnref         func(dev uintptr) uintptr
	hasNatural          func(dev uintptr) int32
	setNatural          func(dev uintptr, on int32) int32
	tapFingers          func(dev uintptr) int32
	setTap              func(dev uintptr, on int32) int32
	setTapMap           func(dev uintptr, m int32) int32
	hasCapability       func(dev uintptr, capability int32) int32
	accelAvailable      func(dev uintptr) int32
	setAccelSpeed       func(dev uintptr, speed float64) int32
	accelProfiles       func(dev uintptr) uint32
	leftHandedAvailable func(dev uintptr) int32
	setLeftHanded       func(dev uintptr, on int32) int32
	setAccelProfile     func(dev uintptr, profile uint32) int32
	iface               [2]uintptr
	active              ports.Seat
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
		reg(&hasCapability, "device_has_capability")
		reg(&accelAvailable, "device_config_accel_is_available")
		reg(&setAccelSpeed, "device_config_accel_set_speed")
		reg(&accelProfiles, "device_config_accel_get_profiles")
		reg(&setAccelProfile, "device_config_accel_set_profile")
		reg(&leftHandedAvailable, "device_config_left_handed_is_available")
		reg(&setLeftHanded, "device_config_left_handed_set")
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
	// Security gates credential logging and emergency quit and stamps input
	// before forwarding. Nil preserves raw unlocked behavior; core remains
	// responsible for input admission.
	Security ports.SessionSecurity
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
	// Devices is the initial config: Touchpad for touchpads, Mouse for the
	// other pointers (mice, trackballs, trackpoints); other devices are left
	// alone. DeviceConfigs replaces it live, on the devices already added.
	Devices       ports.InputDevicesConfig
	DeviceConfigs <-chan ports.InputDevicesConfig
	Active        <-chan bool
	// MoveCursor, when set, places the hardware cursor as soon as motion is
	// read, before core sees the event: the output under the pointer and
	// the physical position on it. motion is false when the pointer is only
	// placed again after a layout or constraint change.
	MoveCursor func(output string, x, y float64, motion bool)
	Log        zerowrap.Logger
	// LogMotion logs every pointer motion (--debug=input-motion); motions
	// are otherwise only counted in the input stats.
	LogMotion bool
	// LogKeys logs every key (--debug=input-keys): typed text, off by
	// default.
	LogKeys bool
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
	p.moved(opts.MoveCursor, false)
	in := &inputState{cfg: opts.Devices, dev: libinputDevices{}, devices: map[uintptr]bool{}}
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
			if opts.Security != nil {
				km.QuarantineFrom(opts.Keymap)
			}
			opts.Keymap.Close()
			opts.Keymap = km
			opts.Log.Info().Msg("keymap replaced")
		case l := <-opts.Layouts:
			p.setLayout(l)
			p.moved(opts.MoveCursor, false)
		case c := <-opts.Constraints:
			p.constrain(c)
			p.moved(opts.MoveCursor, false)
		case c := <-opts.DeviceConfigs:
			in.cfg = c
			for dev := range in.devices {
				in.configure(dev, opts.Log)
			}
			t, m := c.Touchpad, c.Mouse
			opts.Log.Info().Bool("natural_scroll", t.NaturalScroll).Bool("tap", t.Tap).Float64("accel_speed", t.AccelSpeed).Str("accel_profile", t.AccelProfile).Bool("left_handed", t.LeftHanded).Float64("scroll_factor", t.ScrollFactor).
				Bool("mouse_natural_scroll", m.NaturalScroll).Float64("mouse_accel_speed", m.AccelSpeed).Str("mouse_accel_profile", m.AccelProfile).Bool("mouse_left_handed", m.LeftHanded).Msg("input devices reloaded")
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

// inputState is the device state Run owns: the device config, the devices
// it configures (referenced until removed) and the swipe in progress on
// each touchpad. dev applies the config to libinput; it is a zero-size
// type, so storing it in the interface allocates nothing.
type inputState struct {
	cfg     ports.InputDevicesConfig
	dev     deviceConfig
	devices map[uintptr]bool
	swipes  swipes
}

// deviceConfig is libinput's device configuration side: what a device is
// and the settings it takes. libinputDevices is the real one; tests use its
// Mockery mock. It serves device-added and config reload only, never the
// event path. A setting the device lacks is skipped without error; one it
// refuses returns a sentinel error.
type deviceConfig interface {
	// IsTouchpad reports the gesture capability libinput gives touchpads.
	IsTouchpad(dev uintptr) bool
	// IsPointer reports the pointer capability: mice, trackballs,
	// trackpoints and touchpads.
	IsPointer(dev uintptr) bool
	// Name is the device name, for logs.
	Name(dev uintptr) string
	SetNaturalScroll(dev uintptr, on bool) error
	// SetAccel sets the pointer speed and, when the device offers it, the
	// profile (ports.AccelFlat or ports.AccelAdaptive).
	SetAccel(dev uintptr, speed float64, profile string) error
	SetLeftHanded(dev uintptr, on bool) error
	// SetTap enables tap to click, with one finger left, two right, three
	// middle. Devices that cannot tap skip it.
	SetTap(dev uintptr, on bool) error
}

// errRejected is a setting the device refuses (libinput status not success).
var errRejected = errors.New("rejected by libinput")

// libinputDevices implements deviceConfig over the libinput functions.
type libinputDevices struct{}

const (
	// capPointer is LIBINPUT_DEVICE_CAP_POINTER.
	capPointer = 1
	// capGesture is LIBINPUT_DEVICE_CAP_GESTURE: libinput gives it to
	// touchpads, with or without tapping.
	capGesture = 5
	// tapButtonMap is LIBINPUT_CONFIG_TAP_MAP_LRM.
	tapButtonMap = 0
)

func (libinputDevices) IsTouchpad(dev uintptr) bool { return hasCapability(dev, capGesture) != 0 }
func (libinputDevices) IsPointer(dev uintptr) bool  { return hasCapability(dev, capPointer) != 0 }
func (libinputDevices) Name(dev uintptr) string     { return deviceName(dev) }

func (libinputDevices) SetNaturalScroll(dev uintptr, on bool) error {
	if hasNatural(dev) != 0 && setNatural(dev, flag(on)) != 0 {
		return errRejected
	}
	return nil
}

func (libinputDevices) SetAccel(dev uintptr, speed float64, profile string) error {
	if accelAvailable(dev) == 0 {
		return nil
	}
	var err error
	if setAccelSpeed(dev, speed) != 0 {
		err = errRejected
	}
	if p := accelProfile(profile); accelProfiles(dev)&p != 0 && setAccelProfile(dev, p) != 0 {
		err = errRejected
	}
	return err
}

func (libinputDevices) SetLeftHanded(dev uintptr, on bool) error {
	if leftHandedAvailable(dev) != 0 && setLeftHanded(dev, flag(on)) != 0 {
		return errRejected
	}
	return nil
}

func (libinputDevices) SetTap(dev uintptr, on bool) error {
	if tapFingers(dev) <= 0 {
		return nil
	}
	var err error
	if setTap(dev, flag(on)) != 0 {
		err = errRejected
	}
	if setTapMap(dev, tapButtonMap) != 0 {
		err = errRejected
	}
	return err
}

// configurable reports whether the device config applies to dev: a
// touchpad or another pointer. Keyboards, switches and the like are left
// alone and not referenced.
func (s *inputState) configurable(dev uintptr) bool {
	return s.dev.IsTouchpad(dev) || s.dev.IsPointer(dev)
}

// configure applies the config to dev by type: touchpads take the touchpad
// settings and tapping, other pointers (mice, trackballs, trackpoints) the
// mouse settings, other devices nothing. A refused setting is logged and
// the rest still apply. The success path logs and allocates nothing.
func (s *inputState) configure(dev uintptr, log zerowrap.Logger) {
	switch {
	case s.dev.IsTouchpad(dev):
		s.applyPointer(dev, s.cfg.Touchpad.PointerConfig, log)
		s.check(dev, "tap to click", s.dev.SetTap(dev, s.cfg.Touchpad.Tap), log)
	case s.dev.IsPointer(dev):
		s.applyPointer(dev, s.cfg.Mouse, log)
	}
}

// applyPointer applies the settings touchpads and mice share.
func (s *inputState) applyPointer(dev uintptr, c ports.PointerConfig, log zerowrap.Logger) {
	s.check(dev, "natural scroll", s.dev.SetNaturalScroll(dev, c.NaturalScroll), log)
	s.check(dev, "accel", s.dev.SetAccel(dev, c.AccelSpeed, c.AccelProfile), log)
	s.check(dev, "left handed", s.dev.SetLeftHanded(dev, c.LeftHanded), log)
}

// check logs a refused setting; the device name is only read then.
func (s *inputState) check(dev uintptr, setting string, err error, log zerowrap.Logger) {
	if err != nil {
		log.Warn().Err(err).Str("device", s.dev.Name(dev)).Str("setting", setting).Msg("input device setting rejected")
	}
}

// accelProfile is the libinput accel profile bit: LIBINPUT_CONFIG_ACCEL_
// PROFILE_FLAT (1) or _ADAPTIVE (2).
func accelProfile(name string) uint32 {
	if name == ports.AccelFlat {
		return 1
	}
	return 2
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

// swipes streams one three- or four-finger swipe at a time to core, which
// follows
// the fingers. owner is the touchpad of the swipe in progress (0: none);
// another touchpad's swipe is ignored until it ends, so core never sees
// two streams interleaved.
type swipes struct{ owner uintptr }

func (s *swipes) begin(dev uintptr, fingers int, at time.Duration) ports.InputEvent {
	if (fingers != 3 && fingers != 4) || s.owner != 0 {
		return nil
	}
	s.owner = dev
	return ports.SwipeBegin{Fingers: fingers, Time: at}
}

func (s *swipes) update(dev uintptr, dx, dy float64, at time.Duration) ports.InputEvent {
	if dev == 0 || s.owner != dev {
		return nil
	}
	return ports.SwipeUpdate{DX: dx, DY: dy, Time: at}
}

func (s *swipes) end(dev uintptr, cancelled bool, at time.Duration) ports.InputEvent {
	if dev == 0 || s.owner != dev {
		return nil
	}
	s.owner = 0
	return ports.SwipeEnd{Cancelled: cancelled, Time: at}
}

// usec is a libinput timestamp as a duration.
func usec(v uint64) time.Duration { return time.Duration(v) * time.Microsecond }

// msec is a libinput timestamp in wayland's millisecond clock.
func msec(v uint64) uint32 { return uint32(v / 1000) }

// translate converts one libinput event. Timestamps are the device's
// (CLOCK_MONOTONIC), so clients measure speeds (kinetic scrolling) on
// when events happened, not when they were read.
// It snapshots admission before translating and queueing any input kind.
func translate(ev uintptr, opts Options, p *pointer, in *inputState) (ports.InputEvent, error) {
	state := securitySnapshot(opts.Security)
	out, err := translateEvent(ev, opts, p, in, state)
	return secureInput(out, opts.Security, state), err
}

func securitySnapshot(security ports.SessionSecurity) ports.SecurityState {
	if security != nil {
		return security.Snapshot()
	}
	return ports.SecurityState{}
}

func secureInput(ev ports.InputEvent, security ports.SessionSecurity, state ports.SecurityState) ports.InputEvent {
	if ev == nil || security == nil {
		return ev
	}
	return ports.SecurityInput{State: state, Event: ev}
}

func translateEvent(ev uintptr, opts Options, p *pointer, in *inputState, state ports.SecurityState) (ports.InputEvent, error) {
	log := opts.Log
	switch eventType(ev) {
	case evDeviceAdded:
		dev := eventDevice(ev)
		log.Info().Str("device", deviceName(dev)).Msg("input device added")
		if in.configurable(dev) && !in.devices[dev] {
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
		return translateKeyboard(code, pressed, msec(keyboardUsec(k)), opts, state)
	case evPointerMotion:
		pe := pointerEvent(ev)
		m := p.move(pointerDX(pe), pointerDY(pe))
		m.UnaccelDX, m.UnaccelDY = pointerRawDX(pe), pointerRawDY(pe)
		m.TimeUsec = pointerUsec(pe)
		m.TimeMsec = msec(m.TimeUsec)
		p.moved(opts.MoveCursor, true)
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
		p.moved(opts.MoveCursor, true)
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
			if source == ports.AxisFinger {
				// Two-finger scroll is the touchpad's only scroll source.
				s.Value *= in.cfg.Touchpad.ScrollFactor
			}
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

// translateKeyboard applies the production epoch before native translation.
// Quarantined events never reach logging, reserved hotkeys or core.
func translateKeyboard(code uint32, pressed bool, timeMsec uint32, opts Options, state ports.SecurityState) (ports.InputEvent, error) {
	var ke ports.KeyEvent
	if opts.Security != nil {
		var deliver bool
		var err error
		ke, deliver, err = opts.Keymap.KeySecure(code, pressed, timeMsec, state)
		if err != nil || !deliver {
			return nil, err
		}
	} else {
		ke = opts.Keymap.Key(code, pressed, timeMsec)
	}
	return translateKey(ke, opts, state)
}

// translateKey applies the keyboard policy without native calls. One coherent
// snapshot governs both logging and reserved hotkeys; protected keys still go
// to core for the locker. VT switching is allowed and does not release the lock.
func translateKey(ke ports.KeyEvent, opts Options, security ports.SecurityState) (ports.InputEvent, error) {
	if opts.LogKeys && !security.Protected {
		opts.Log.Debug().Str("component", "input").Uint32("code", ke.Keycode).Str("keysym", ke.Keysym).Bool("pressed", ke.Pressed).Uint8("mods", uint8(ke.Mods)).Msg("key")
	}
	switch action, vt := hotkey(ke, security.Protected); action {
	case hotkeyQuit:
		opts.Log.Warn().Str("component", "input").Str("reason", "emergency-key").Msg("quit")
		return nil, ErrEmergencyQuit
	case hotkeyVT:
		opts.Seat.SwitchVT(vt)
		return nil, nil
	}
	return ke, nil
}

type hotkeyAction int

const (
	hotkeyNone hotkeyAction = iota
	hotkeyQuit
	hotkeyVT
)

// hotkey detects compositor-reserved keys on press: Ctrl+Alt+Backspace and
// Ctrl+Alt+F1..F12. Protection disables emergency quit, but not VT switching.
func hotkey(ke ports.KeyEvent, protected bool) (hotkeyAction, int) {
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
		if !protected {
			return hotkeyQuit, 0
		}
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
// pointer moves inside. A warp moves it to core's cursor.
func (p *pointer) constrain(c ports.PointerConstraint) {
	if c.Warp || c.Mode == ports.ConstraintLock || p.constraint.Mode == ports.ConstraintLock {
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

// moved reports the physical position on the output under the pointer;
// motion tells the device moved it.
func (p *pointer) moved(move func(output string, x, y float64, motion bool), motion bool) {
	if o, ok := p.layout.At(p.x, p.y); ok && move != nil {
		move(o.Info.Name, (p.x-float64(o.X))*o.Scale, (p.y-float64(o.Y))*o.Scale, motion)
	}
}
