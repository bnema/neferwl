// Package screensaver serves org.freedesktop.ScreenSaver on the session bus,
// so programs that inhibit idle over D-Bus (browsers, video players, the GTK
// portal's Inhibit) keep the session awake like a zwp_idle_inhibit_v1
// surface. It only reports whether any inhibition is held: wayland decides
// what that holds back.
package screensaver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

const (
	busName = "org.freedesktop.ScreenSaver"
	iface   = "org.freedesktop.ScreenSaver"
	// callTimeout bounds the bus calls Run makes itself.
	callTimeout = 3 * time.Second
)

// paths: freedesktop clients use the first, older KDE-era ones the second.
var paths = []dbus.ObjectPath{"/org/freedesktop/ScreenSaver", "/ScreenSaver"}

var methods = []introspect.Method{
	{Name: "Inhibit", Args: []introspect.Arg{{Name: "application_name", Type: "s", Direction: "in"}, {Name: "reason_for_inhibit", Type: "s", Direction: "in"}, {Name: "cookie", Type: "u", Direction: "out"}}},
	{Name: "UnInhibit", Args: []introspect.Arg{{Name: "cookie", Type: "u", Direction: "in"}}},
	{Name: "GetActive", Args: []introspect.Arg{{Type: "b", Direction: "out"}}},
}

// Service owns one private session bus connection. Run must be called once.
type Service struct {
	conn     *dbus.Conn
	log      zerowrap.Logger
	requests chan request
	done     chan struct{}
}

// request is a method call handed from a godbus goroutine to Run.
type request struct {
	sender string
	cookie uint32 // UnInhibit
	unhold bool
	app    string
	reason string
	reply  chan reply
}

type reply struct {
	cookie uint32
	err    *dbus.Error
}

// New connects to the bus at address, the session bus when empty. It never
// starts a bus.
func New(ctx context.Context, address string, log zerowrap.Logger) (*Service, error) {
	var conn *dbus.Conn
	var err error
	if address == "" {
		conn, err = dbus.SessionBusPrivateNoAutoStartup(dbus.WithContext(ctx))
	} else {
		conn, err = dbus.Dial(address, dbus.WithContext(ctx))
	}
	if err != nil {
		return nil, fmt.Errorf("session bus: %w", err)
	}
	if err = conn.Auth(nil); err == nil {
		err = conn.Hello()
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("session bus: %w", err)
	}
	return &Service{conn: conn, log: log, requests: make(chan request), done: make(chan struct{})}, nil
}

// Run serves the name until ctx ends or the bus goes, and sends on held each
// change of whether any inhibition is in effect. If the bus goes while an
// inhibition is held, it sends false before it returns. Another owner of the name is not an error:
// Run then serves nothing and waits for ctx.
func (s *Service) Run(ctx context.Context, held chan<- bool) error {
	defer close(s.done)
	defer func() { _ = s.conn.Close() }()
	signals := make(chan *dbus.Signal, 16)
	s.conn.Signal(signals)
	// Leaving connections are how inhibitions of crashed clients end.
	if err := s.conn.AddMatchSignalContext(ctx, dbus.WithMatchSender("org.freedesktop.DBus"), dbus.WithMatchInterface("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged")); err != nil {
		return fmt.Errorf("watch bus names: %w", err)
	}
	for _, p := range paths {
		if err := s.conn.Export(handler{s}, p, iface); err != nil {
			return err
		}
		node := &introspect.Node{Name: string(p), Interfaces: []introspect.Interface{{Name: iface, Methods: methods}}}
		if err := s.conn.Export(introspect.NewIntrospectable(node), p, "org.freedesktop.DBus.Introspectable"); err != nil {
			return err
		}
	}
	r, err := s.conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return fmt.Errorf("request %s: %w", busName, err)
	}
	if r != dbus.RequestNameReplyPrimaryOwner {
		s.log.Warn().Msg(busName + " is owned by another program; D-Bus idle inhibitors are left to it")
		<-ctx.Done()
		return nil
	}
	s.log.Info().Msg("serving " + busName)
	reg := newRegistry()
	sent := false
	report := func() {
		if h := reg.held(); h != sent {
			select {
			case held <- h:
				sent = h
			case <-ctx.Done():
			}
		}
	}
	defer func() {
		if sent && ctx.Err() == nil {
			// The bus went, the session goes on: nothing it held keeps it awake.
			select {
			case held <- false:
			case <-ctx.Done():
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case sig, ok := <-signals:
			if !ok {
				if ctx.Err() != nil {
					return nil // WithContext closed the connection
				}
				return errors.New("session bus connection closed")
			}
			if name, gone := left(sig); gone {
				reg.drop(name)
				report()
			}
		case req := <-s.requests:
			if req.unhold {
				if reg.uninhibit(req.sender, req.cookie) {
					s.log.Debug().Str("sender", req.sender).Uint32("cookie", req.cookie).Msg("uninhibit")
				}
				req.reply <- reply{}
				report()
				continue
			}
			cookie, first, ok := reg.inhibit(req.sender)
			if !ok {
				req.reply <- reply{err: dbus.MakeFailedError(errors.New("too many inhibitors"))}
				continue
			}
			req.reply <- reply{cookie: cookie}
			if first && !s.onBus(ctx, req.sender) {
				// It left before its call reached us: its signal came first.
				reg.drop(req.sender)
			} else {
				s.log.Debug().Str("sender", req.sender).Str("app", req.app).Str("reason", req.reason).Uint32("cookie", cookie).Msg("inhibit")
			}
			report()
		}
	}
}

// onBus reports whether a unique connection name still has an owner. An
// unanswered query keeps the inhibition: its departure signal still ends it.
func (s *Service) onBus(ctx context.Context, name string) bool {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var has bool
	if err := s.conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, name).Store(&has); err != nil {
		return true
	}
	return has
}

// left reports a unique connection name that lost its owner.
func left(sig *dbus.Signal) (string, bool) {
	if sig.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(sig.Body) != 3 {
		return "", false
	}
	name, _ := sig.Body[0].(string)
	owner, _ := sig.Body[2].(string)
	return name, strings.HasPrefix(name, ":") && owner == ""
}

// call hands a request to Run, or fails once Run has stopped.
func (s *Service) call(req request) reply {
	req.reply = make(chan reply, 1)
	select {
	case s.requests <- req:
		return <-req.reply
	case <-s.done:
		return reply{err: dbus.MakeFailedError(errors.New("screensaver service stopped"))}
	}
}

// handler is the exported object; godbus runs each call on its own
// goroutine, so it only forwards to Run.
type handler struct{ s *Service }

func (h handler) Inhibit(sender dbus.Sender, app, reason string) (uint32, *dbus.Error) {
	r := h.s.call(request{sender: string(sender), app: app, reason: reason})
	return r.cookie, r.err
}

func (h handler) UnInhibit(sender dbus.Sender, cookie uint32) *dbus.Error {
	return h.s.call(request{sender: string(sender), cookie: cookie, unhold: true}).err
}

// GetActive: there is no screensaver to be active, only idle inhibition.
func (handler) GetActive() (bool, *dbus.Error) { return false, nil }
