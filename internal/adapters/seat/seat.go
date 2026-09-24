// Package seat opens a libseat session (logind or seatd) and hands out device fds.
package seat

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"unsafe"

	"github.com/bnema/purego"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

var (
	loadOnce sync.Once
	loadErr  error

	openSeat      func(listener unsafe.Pointer, data uintptr) uintptr
	closeSeat     func(seat uintptr) int32
	disableSeat   func(seat uintptr) int32
	openDevice    func(seat uintptr, path string, fd *int32) int32
	closeDevice   func(seat uintptr, id int32) int32
	seatName      func(seat uintptr) string
	switchSession func(seat uintptr, session int32) int32
	getFD         func(seat uintptr) int32
	dispatch      func(seat uintptr, timeout int32) int32

	listener [2]uintptr
	// libseat allows one seat per process; callbacks find it here.
	current *Seat
)

func load() error {
	loadOnce.Do(func() {
		lib, err := purego.Dlopen("libseat.so.1", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			loadErr = fmt.Errorf("open libseat.so.1: %w", err)
			return
		}
		defer func() {
			if r := recover(); r != nil {
				loadErr = fmt.Errorf("libseat: %v", r)
			}
		}()
		reg := func(fn any, name string) { purego.RegisterLibFunc(fn, lib, "libseat_"+name) }
		reg(&openSeat, "open_seat")
		reg(&closeSeat, "close_seat")
		reg(&disableSeat, "disable_seat")
		reg(&openDevice, "open_device")
		reg(&closeDevice, "close_device")
		reg(&seatName, "seat_name")
		reg(&switchSession, "switch_session")
		reg(&getFD, "get_fd")
		reg(&dispatch, "dispatch")
		listener[0] = purego.NewCallback(func(_, _ uintptr) { current.changed(true) })
		listener[1] = purego.NewCallback(func(_, _ uintptr) { current.changed(false) })
	})
	return loadErr
}

// Seat owns the libseat handle. libseat is not thread-safe, so every call goes
// through mu; callbacks only run inside dispatch, which already holds it.
type Seat struct {
	mu      sync.Mutex
	handle  uintptr
	active  bool
	devices map[int32]int32 // fd → libseat device id
	subs    []chan bool
	log     zerowrap.Logger
}

// Open connects to logind or seatd and waits for the first enable.
func Open(ctx context.Context, log zerowrap.Logger) (*Seat, error) {
	if err := load(); err != nil {
		return nil, err
	}
	if current != nil {
		return nil, errors.New("seat already open")
	}
	s := &Seat{devices: make(map[int32]int32), log: log}
	current = s
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handle = openSeat(unsafe.Pointer(&listener), 0)
	runtime.KeepAlive(&listener)
	if s.handle == 0 {
		current = nil
		return nil, errors.New("libseat_open_seat failed: no logind/seatd session for this TTY (run from a text console you are logged into)")
	}
	log.Info().Str("seat", seatName(s.handle)).Msg("seat opened")
	for !s.active {
		if err := s.dispatchLocked(ctx, 100); err != nil {
			s.closeLocked()
			return nil, fmt.Errorf("waiting for seat enable: %w", err)
		}
	}
	return s, nil
}

func (s *Seat) changed(active bool) {
	s.active = active
	s.log.Info().Bool("active", active).Msg("seat")
	if !active {
		// Devices are revoked by the seat manager; ack so the VT can switch.
		disableSeat(s.handle)
	}
	for _, c := range s.subs {
		select {
		case c <- active:
		default:
			select {
			case <-c:
			default:
			}
			c <- active
		}
	}
}

// Name returns the seat name, e.g. seat0.
func (s *Seat) Name() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return seatName(s.handle)
}

// Subscribe returns a channel that receives the seat state after every enable/disable.
// The current state is sent first.
func (s *Seat) Subscribe() <-chan bool {
	c := make(chan bool, 1)
	s.mu.Lock()
	c <- s.active
	s.subs = append(s.subs, c)
	s.mu.Unlock()
	return c
}

// Unsubscribe stops sending to a channel from Subscribe.
func (s *Seat) Unsubscribe(c <-chan bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs = slices.DeleteFunc(s.subs, func(x chan bool) bool { return x == c })
}

func (s *Seat) dispatchLocked(ctx context.Context, timeoutMs int) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	fds := []unix.PollFd{{Fd: getFD(s.handle), Events: unix.POLLIN}}
	if _, err := unix.Poll(fds, timeoutMs); err != nil && !errors.Is(err, unix.EINTR) {
		return err
	}
	if dispatch(s.handle, 0) < 0 {
		return errors.New("libseat_dispatch failed")
	}
	return nil
}

// Run dispatches seat events until ctx ends. The owner calls Close afterwards,
// once outputs have restored their CRTC state.
func (s *Seat) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		fds := []unix.PollFd{{Fd: getFD(s.handle), Events: unix.POLLIN}}
		if _, err := unix.Poll(fds, 100); err != nil && !errors.Is(err, unix.EINTR) {
			return fmt.Errorf("seat poll: %w", err)
		}
		// Dispatch every tick: libseat may queue events without the fd becoming readable.
		s.mu.Lock()
		r := dispatch(s.handle, 0)
		s.mu.Unlock()
		if r < 0 {
			return errors.New("libseat_dispatch failed")
		}
	}
	return nil
}

// OpenDevice opens path through the seat manager and returns its fd.
func (s *Seat) OpenDevice(path string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var fd int32 = -1
	id := openDevice(s.handle, path, &fd)
	if id < 0 {
		s.log.Warn().Str("path", path).Msg("open device failed")
		return -1, fmt.Errorf("libseat_open_device %s failed", path)
	}
	s.devices[fd] = id
	s.log.Info().Str("path", path).Int32("fd", fd).Msg("device opened")
	return int(fd), nil
}

// CloseDevice releases a fd returned by OpenDevice.
func (s *Seat) CloseDevice(fd int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.devices[int32(fd)]
	if !ok {
		return
	}
	delete(s.devices, int32(fd))
	closeDevice(s.handle, id)
	_ = unix.Close(fd)
	s.log.Info().Int("fd", fd).Msg("device closed")
}

// SwitchVT asks the seat manager to switch to virtual terminal vt.
func (s *Seat) SwitchVT(vt int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log.Info().Int("vt", vt).Msg("switch vt")
	if switchSession(s.handle, int32(vt)) < 0 {
		s.log.Warn().Int("vt", vt).Msg("switch vt failed")
	}
}

// Close releases all devices and the seat; the VT returns to text mode. Safe to call twice.
func (s *Seat) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
}

func (s *Seat) closeLocked() {
	if s.handle == 0 {
		return
	}
	for fd, id := range s.devices {
		closeDevice(s.handle, id)
		_ = unix.Close(int(fd))
	}
	s.devices = map[int32]int32{}
	closeSeat(s.handle)
	s.handle = 0
	current = nil
	s.log.Info().Msg("seat closed")
}
