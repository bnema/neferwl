// Package directseat opens input devices directly, without logind or
// seatd. It serves server mode under systemd linger, where no session
// broker exists. Each device is grabbed exclusively (EVIOCGRAB) so
// keystrokes injected through uinput do not reach the kernel console.
package directseat

import (
	"fmt"

	"bnema.dev/zerowrap"
	"golang.org/x/sys/unix"

	"github.com/bnema/neferwl/internal/ports"
)

// name is the seat every directly opened device belongs to.
const name = "seat0"

// eviocgrab is EVIOCGRAB, _IOW('E', 0x90, int); x/sys/unix does not export it.
const eviocgrab = 0x40044590

// evdev is the kernel boundary for input device fds.
type evdev interface {
	open(path string) (int, error)
	grab(fd int) error
	ungrab(fd int) error
	close(fd int) error
}

type unixEvdev struct{}

func (unixEvdev) open(path string) (int, error) {
	return unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
}

func (unixEvdev) grab(fd int) error   { return unix.IoctlSetInt(fd, eviocgrab, 1) }
func (unixEvdev) ungrab(fd int) error { return unix.IoctlSetInt(fd, eviocgrab, 0) }
func (unixEvdev) close(fd int) error  { return unix.Close(fd) }

// Seat implements ports.Seat by opening devices itself. It is always
// active: there is no VT to switch away from.
type Seat struct {
	log    zerowrap.Logger
	dev    evdev
	active chan bool
}

var _ ports.Seat = (*Seat)(nil)

// New returns a direct seat. log should carry the seat component.
func New(log zerowrap.Logger) *Seat { return newSeat(log, unixEvdev{}) }

func newSeat(log zerowrap.Logger, dev evdev) *Seat {
	active := make(chan bool, 1)
	active <- true
	return &Seat{log: log, dev: dev, active: active}
}

// OpenDevice opens path read-write and grabs it exclusively. A failed grab
// is logged and the fd kept: the device still works, only shared.
func (s *Seat) OpenDevice(path string) (int, error) {
	fd, err := s.dev.open(path)
	if err != nil {
		return -1, fmt.Errorf("open input device %s: %w", path, err)
	}
	if err := s.dev.grab(fd); err != nil {
		s.log.Warn().Err(err).Str("path", path).Msg("exclusive grab failed; device stays shared")
	}
	return fd, nil
}

// CloseDevice releases the grab and closes fd.
func (s *Seat) CloseDevice(fd int) {
	if err := s.dev.ungrab(fd); err != nil {
		s.log.Debug().Err(err).Int("fd", fd).Msg("release grab")
	}
	if err := s.dev.close(fd); err != nil {
		s.log.Warn().Err(err).Int("fd", fd).Msg("close input device")
	}
}

// SwitchVT is a no-op: a direct seat owns no VT.
func (s *Seat) SwitchVT(vt int) {
	s.log.Debug().Int("vt", vt).Msg("vt switch ignored without a session broker")
}

// Name returns the seat name.
func (s *Seat) Name() string { return name }

// Active yields true once: the seat never goes inactive.
func (s *Seat) Active() <-chan bool { return s.active }
