// Package screensaver serves org.freedesktop.ScreenSaver idle inhibition.
package screensaver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bnema/neferwl/internal/adapters/busretry"
	"github.com/bnema/zerobus"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

const (
	busName     = "org.freedesktop.ScreenSaver"
	iface       = busName
	busDaemon   = "org.freedesktop.DBus"
	callTimeout = 3 * time.Second
)

var ErrNameTaken = errors.New(busName + " is owned by another program")

// Reports are what Run tells wayland. Held carries each change of whether
// any inhibition is in effect; Activity carries SimulateUserActivity calls,
// coalesced: a send never blocks, so give it a buffer of one.
type Reports struct {
	Held     chan<- bool
	Activity chan<- struct{}
}

// Serve runs the service until ctx ends. When the bus goes or another
// program owns the name, it tries again after retry, doubling up to
// a minute, so a restarted bus or a released name is picked up.
func Serve(ctx context.Context, address string, r Reports, retry time.Duration, log zerowrap.Logger) {
	busretry.Run(ctx, retry, "D-Bus idle inhibitors", log, func(ctx context.Context) error {
		s, err := New(ctx, address, log)
		if err != nil {
			return err
		}
		return s.Run(ctx, r)
	})
}

// Service owns one private connection. Run must be called once. All message
// decoding and encoding belongs to Run; only Close may run concurrently.
type Service struct {
	conn bus
	log  zerowrap.Logger
}

func New(ctx context.Context, address string, log zerowrap.Logger) (*Service, error) {
	var c *zerobus.Conn
	var err error
	if address == "" {
		c, err = zerobus.SessionBusContext(ctx)
	} else {
		c, err = zerobus.DialContext(ctx, address)
	}
	if err != nil {
		return nil, fmt.Errorf("session bus: %w", err)
	}
	return &Service{conn: c, log: log}, nil
}

type ownerCheck struct {
	name     string
	deadline time.Time
	retried  bool
}

// Run reports changes until cancellation or connection loss. Owner queries
// are asynchronous: synchronous Call would consume incoming client calls.
func (s *Service) Run(ctx context.Context, reports Reports) (err error) {
	stopped := make(chan struct{})
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			_ = s.conn.Close()
		case <-stopped:
		}
	}()
	defer func() {
		close(stopped)
		<-joined
		_ = s.conn.Close()
		if ctx.Err() != nil {
			err = nil
		}
	}()
	if err := s.conn.AddMatch("type='signal',sender='org.freedesktop.DBus',interface='org.freedesktop.DBus',member='NameOwnerChanged',arg2=''"); err != nil {
		return fmt.Errorf("watch bus names: %w", err)
	}
	// No synchronous bus calls after acquiring the public name: Call can
	// discard client messages while waiting for its own reply.
	owner, err := s.conn.RequestName(busName, zerobus.NameFlagDoNotQueue)
	if err != nil {
		return fmt.Errorf("request %s: %w", busName, err)
	}
	if owner != zerobus.NamePrimaryOwner {
		return ErrNameTaken
	}
	raw, err := s.conn.SyscallConn()
	if err != nil {
		return err
	}
	s.log.Info().Msg("serving " + busName)
	reg := newRegistry()
	sent := false
	report := func() {
		if held := reg.held(); held != sent {
			select {
			case reports.Held <- held:
				sent = held
			case <-ctx.Done():
			}
		}
	}
	defer func() {
		if sent && ctx.Err() == nil {
			select {
			case reports.Held <- false:
			case <-ctx.Done():
			}
		}
	}()
	checks := map[uint32]ownerCheck{}
	ask := func(name string, retried bool) error {
		e, serial := s.conn.NewMethodCall(busDaemon, "/org/freedesktop/DBus", busDaemon, "NameHasOwner", "s")
		e.Str(name)
		if _, err := s.conn.Send(); err != nil {
			return err
		}
		checks[serial] = ownerCheck{name: name, deadline: time.Now().Add(callTimeout), retried: retried}
		return nil
	}
	retry := func(check ownerCheck) error {
		if !check.retried {
			return ask(check.name, true)
		}
		reg.drop(check.name)
		report()
		return nil
	}
	for ctx.Err() == nil {
		for serial, check := range checks {
			if !time.Now().Before(check.deadline) {
				delete(checks, serial)
				if err := retry(check); err != nil {
					return err
				}
			}
		}
		if !s.conn.Buffered() {
			n := 0
			var pollErr error
			err := raw.Control(func(fd uintptr) {
				fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
				n, pollErr = unix.Poll(fds, 50)
			})
			if err != nil {
				return err
			}
			if pollErr == unix.EINTR {
				continue
			}
			if pollErr != nil {
				return pollErr
			}
			if n == 0 {
				continue
			}
		}
		m, err := s.conn.ReadMessage()
		if err != nil {
			return err
		}
		switch m.Type {
		case zerobus.TypeSignal:
			if m.Sender == busDaemon && m.Interface == busDaemon && m.Member == "NameOwnerChanged" && m.Signature == "sss" {
				r := m.Body()
				name := r.Str()
				_ = r.Str()
				owner := r.Str()
				if r.Err() == nil && strings.HasPrefix(name, ":") && owner == "" {
					reg.drop(name)
					for serial, c := range checks {
						if c.name == name {
							delete(checks, serial)
						}
					}
					report()
				}
			}
		case zerobus.TypeMethodReturn, zerobus.TypeError:
			if m.Sender != busDaemon {
				continue
			}
			check, ok := checks[m.ReplySerial]
			if !ok {
				continue
			}
			delete(checks, m.ReplySerial)
			if m.Type == zerobus.TypeMethodReturn && m.Signature == "b" {
				r := m.Body()
				has := r.Bool()
				if r.Err() == nil {
					if !has {
						reg.drop(check.name)
						report()
					}
					continue
				}
			}
			if err := retry(check); err != nil {
				return err
			}
		case zerobus.TypeMethodCall:
			// Dispatch consumes the receive buffer before the next read.
			name, first, err := s.dispatch(m, reg, reports.Activity)
			if err != nil {
				return err
			}
			if first {
				if err := ask(name, false); err != nil {
					return err
				}
			}
			report()
		}
	}
	return nil
}

func (s *Service) dispatch(m *zerobus.Message, reg *registry, activity chan<- struct{}) (string, bool, error) {
	if m.Sender == "" {
		return "", false, nil
	}
	sig := ""
	errorName := ""
	text := ""
	var cookie uint32
	first := false
	fail := func(name, msg string) { errorName = "org.freedesktop.DBus.Error." + name; text = msg }
	validPath := m.Path == "/org/freedesktop/ScreenSaver" || m.Path == "/ScreenSaver"
	want := ""
	known := validPath && (m.Interface == iface || m.Interface == "")
	if validPath && m.Interface == "org.freedesktop.DBus.Introspectable" && m.Member == "Introspect" {
		sig = "s"
	} else if known {
		switch m.Member {
		case "Inhibit":
			want = "ss"
			sig = "u"
		case "UnInhibit":
			want = "u"
		case "GetActive", "SetActive":
			sig = "b"
			if m.Member == "SetActive" {
				want = "b"
			}
		case "GetActiveTime":
			sig = "u"
		case "SimulateUserActivity":
		default:
			known = false
		}
	} else {
		known = false
	}
	if sig != "s" && !known {
		fail("UnknownMethod", "unknown method")
	} else if m.Signature != want {
		fail("InvalidArgs", "unexpected arguments")
	} else {
		r := m.Body()
		switch m.Member {
		case "Inhibit":
			_ = r.Str()
			_ = r.Str()
			if r.Err() == nil {
				var ok bool
				cookie, first, ok = reg.inhibit(strings.Clone(m.Sender))
				if !ok {
					fail("Failed", "too many inhibitors")
				}
			}
		case "UnInhibit":
			cookie = r.Uint32()
			if r.Err() == nil {
				reg.uninhibit(m.Sender, cookie)
			}
		case "SetActive":
			_ = r.Bool()
		case "SimulateUserActivity":
			select {
			case activity <- struct{}{}:
			default:
			}
		}
		if r.Err() != nil {
			fail("InvalidArgs", "invalid arguments")
		}
	}
	name := strings.Clone(m.Sender)
	if m.Flags&zerobus.FlagNoReplyExpected != 0 {
		return name, first, nil
	}
	if errorName != "" {
		s.conn.NewError(m, errorName, "s").Str(text)
	} else {
		e := s.conn.NewReply(m, sig)
		switch sig {
		case "s":
			e.Str(introspection(m.Path))
		case "u":
			if m.Member == "Inhibit" {
				e.Uint32(cookie)
			} else {
				e.Uint32(0)
			}
		case "b":
			e.Bool(false)
		}
	}
	_, err := s.conn.Send()
	return name, first, err
}

func introspection(path string) string {
	return `<node name="` + path + `"><interface name="org.freedesktop.DBus.Introspectable"><method name="Introspect"><arg type="s" direction="out"/></method></interface><interface name="org.freedesktop.ScreenSaver"><method name="Inhibit"><arg name="application_name" type="s" direction="in"/><arg name="reason_for_inhibit" type="s" direction="in"/><arg name="cookie" type="u" direction="out"/></method><method name="UnInhibit"><arg name="cookie" type="u" direction="in"/></method><method name="GetActive"><arg type="b" direction="out"/></method><method name="GetActiveTime"><arg type="u" direction="out"/></method><method name="SetActive"><arg name="e" type="b" direction="in"/><arg type="b" direction="out"/></method><method name="SimulateUserActivity"></method></interface></node>`
}
