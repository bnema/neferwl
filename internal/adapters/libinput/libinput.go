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

	"github.com/bnema/nefertty/internal/adapters/xkb"
	"github.com/bnema/nefertty/internal/ports"
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
	pointerEvent    func(ev uintptr) uintptr
	pointerDX       func(pev uintptr) float64
	pointerDY       func(pev uintptr) float64
	pointerAbsX     func(pev uintptr, width uint32) float64
	pointerAbsY     func(pev uintptr, height uint32) float64
	pointerButton   func(pev uintptr) uint32
	pointerBtnState func(pev uintptr) int32
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
		reg(&pointerEvent, "event_get_pointer_event")
		reg(&pointerDX, "event_pointer_get_dx")
		reg(&pointerDY, "event_pointer_get_dy")
		reg(&pointerAbsX, "event_pointer_get_absolute_x_transformed")
		reg(&pointerAbsY, "event_pointer_get_absolute_y_transformed")
		reg(&pointerButton, "event_pointer_get_button")
		reg(&pointerBtnState, "event_pointer_get_button_state")
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
	Active  <-chan bool
	// MoveCursor, when set, places the hardware cursor as soon as motion is
	// read, before core sees the event: the output under the pointer and
	// the physical position on it.
	MoveCursor func(output string, x, y float64)
	Log        zerowrap.Logger
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
	fd := getFD(li)
	fwd := newForwarder()
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
			out, err := translate(ev, opts, p)
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

func translate(ev uintptr, opts Options, p *pointer) (ports.InputEvent, error) {
	now := uint32(time.Now().UnixMilli())
	log := opts.Log
	switch eventType(ev) {
	case evDeviceAdded:
		log.Info().Str("device", deviceName(eventDevice(ev))).Msg("input device added")
	case evDeviceRemoved:
		log.Info().Str("device", deviceName(eventDevice(ev))).Msg("input device removed")
	case evKeyboardKey:
		k := keyboardEvent(ev)
		code, pressed := keyboardKey(k), keyboardState(k) == 1
		ke := opts.Keymap.Key(code, pressed, now)
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
		x, y := p.move(pointerDX(pe), pointerDY(pe))
		p.moved(opts.MoveCursor)
		log.Debug().Float64("x", x).Float64("y", y).Msg("pointer")
		return ports.PointerMotion{X: x, Y: y, TimeMsec: now}, nil
	case evPointerAbs:
		// Absolute devices (tablets, VMs) map to the whole layout.
		pe := pointerEvent(ev)
		b := p.bounds()
		x, y := p.set(float64(b.X)+pointerAbsX(pe, uint32(b.W)), float64(b.Y)+pointerAbsY(pe, uint32(b.H)))
		p.moved(opts.MoveCursor)
		return ports.PointerMotion{X: x, Y: y, TimeMsec: now}, nil
	case evPointerButton:
		pe := pointerEvent(ev)
		b, pressed := pointerButton(pe), pointerBtnState(pe) == 1
		log.Debug().Uint32("button", b).Bool("pressed", pressed).Msg("button")
		return ports.PointerButton{Button: b, Pressed: pressed, TimeMsec: now}, nil
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
	x, y   float64
	layout ports.Layout
}

// newPointer starts at the centre of the first output.
func newPointer(l ports.Layout) *pointer {
	p := &pointer{layout: l}
	if len(l) > 0 {
		p.x, p.y = float64(l[0].X)+float64(l[0].Width)/2, float64(l[0].Y)+float64(l[0].Height)/2
	}
	return p
}

// setLayout keeps the pointer where it is, or clamps it onto an output.
func (p *pointer) setLayout(l ports.Layout) {
	p.layout = l
	p.x, p.y = l.Clamp(p.x, p.y, p.x, p.y)
}

func (p *pointer) scale() float64 {
	if o, ok := p.layout.At(p.x, p.y); ok && o.Scale > 0 {
		return o.Scale
	}
	return 1
}

func (p *pointer) move(dx, dy float64) (float64, float64) {
	s := p.scale()
	return p.set(p.x+dx/s, p.y+dy/s)
}

func (p *pointer) set(x, y float64) (float64, float64) {
	p.x, p.y = p.layout.Clamp(p.x, p.y, x, y)
	return p.x, p.y
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
