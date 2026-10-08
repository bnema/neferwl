// Package screensaver serves org.freedesktop.ScreenSaver on the session bus,
// so programs that inhibit idle over D-Bus (browsers, video players, the GTK
// portal's Inhibit) keep the session awake like a zwp_idle_inhibit_v1
// surface. It only reports whether any inhibition is held, and activity
// simulated by SimulateUserActivity: wayland decides what they do.
package screensaver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bnema/neferwl/internal/adapters/busretry"
	"github.com/bnema/zerowrap"
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

const (
	busName = "org.freedesktop.ScreenSaver"
	iface   = "org.freedesktop.ScreenSaver"
	// busDaemon is the sender of the bus's own signals. Any client can send
	// a NameOwnerChanged to us directly, so only this sender is trusted.
	busDaemon = "org.freedesktop.DBus"
	// callTimeout bounds the bus calls Run makes itself.
	callTimeout = 3 * time.Second
)

// ErrNameTaken is returned by Run when another program owns the name.
var ErrNameTaken = errors.New(busName + " is owned by another program")

// paths: freedesktop clients use the first, older KDE-era ones the second.
var paths = []dbus.ObjectPath{"/org/freedesktop/ScreenSaver", "/ScreenSaver"}

var methods = []introspect.Method{
	{Name: "Inhibit", Args: []introspect.Arg{{Name: "application_name", Type: "s", Direction: "in"}, {Name: "reason_for_inhibit", Type: "s", Direction: "in"}, {Name: "cookie", Type: "u", Direction: "out"}}},
	{Name: "UnInhibit", Args: []introspect.Arg{{Name: "cookie", Type: "u", Direction: "in"}}},
	{Name: "GetActive", Args: []introspect.Arg{{Type: "b", Direction: "out"}}},
	{Name: "GetActiveTime", Args: []introspect.Arg{{Type: "u", Direction: "out"}}},
	{Name: "SetActive", Args: []introspect.Arg{{Name: "e", Type: "b", Direction: "in"}, {Type: "b", Direction: "out"}}},
	{Name: "SimulateUserActivity"},
}

// Reports are what Run tells wayland. Held carries each change of whether
// any inhibition is in effect; Activity carries SimulateUserActivity calls,
// coalesced: a send never blocks, so give it a buffer of one.
type Reports struct {
	Held     chan<- bool
	Activity chan<- struct{}
}

// Serve runs the service until ctx ends. When the bus goes or another
// program owns the name, it tries again after retry, doubling up to
// busretry.Max, so a restarted bus or a released name is picked up.
func Serve(ctx context.Context, address string, r Reports, retry time.Duration, log zerowrap.Logger) {
	busretry.Run(ctx, retry, "D-Bus idle inhibitors", log, func(ctx context.Context) error {
		s, err := New(ctx, address, log)
		if err != nil {
			return err
		}
		return s.Run(ctx, r)
	})
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

// Run serves the name until ctx ends or the bus goes, and reports to r. If
// the bus goes while an inhibition is held, it sends false on r.Held before
// it returns. It returns nil only once ctx ends, and ErrNameTaken at once,
// with the connection closed, when another program owns the name.
func (s *Service) Run(ctx context.Context, r Reports) (err error) {
	defer close(s.done)
	defer func() { _ = s.conn.Close() }()
	defer func() {
		// A bus call cut short by ctx (WithContext closes the connection)
		// is shutdown, not a failure.
		if ctx.Err() != nil {
			err = nil
		}
	}()
	// Method tables, not Export(v): godbus finds Export's methods by
	// reflection, which stops the linker from pruning unused methods and
	// grows the binary by ~4 MB.
	h := handler{s: s, activity: r.Activity}
	table := map[string]any{
		"Inhibit":              h.Inhibit,
		"UnInhibit":            h.UnInhibit,
		"GetActive":            h.GetActive,
		"GetActiveTime":        h.GetActiveTime,
		"SetActive":            h.SetActive,
		"SimulateUserActivity": h.SimulateUserActivity,
	}
	for _, p := range paths {
		if err := s.conn.ExportMethodTable(table, p, iface); err != nil {
			return fmt.Errorf("export %s: %w", p, err)
		}
		node := &introspect.Node{Name: string(p), Interfaces: []introspect.Interface{{Name: iface, Methods: methods}}}
		xml := string(introspect.NewIntrospectable(node))
		intro := map[string]any{"Introspect": func() (string, *dbus.Error) { return xml, nil }}
		if err := s.conn.ExportMethodTable(intro, p, "org.freedesktop.DBus.Introspectable"); err != nil {
			return fmt.Errorf("export %s introspection: %w", p, err)
		}
	}
	owner, err := s.conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return fmt.Errorf("request %s: %w", busName, err)
	}
	if owner != dbus.RequestNameReplyPrimaryOwner {
		return ErrNameTaken
	}
	// Departures end the inhibitions of crashed clients. The match asks the
	// bus for lost owners (new owner "") only, but the channel also gets
	// NameAcquired and any signal a client sends to us directly: left trusts
	// only the bus's own. A client that leaves before this match exists is
	// caught by the onBus check on its first cookie.
	signals := make(chan *dbus.Signal, 16)
	s.conn.Signal(signals)
	if err := s.conn.AddMatchSignalContext(ctx, dbus.WithMatchSender(busDaemon), dbus.WithMatchInterface("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(2, "")); err != nil {
		return fmt.Errorf("watch bus names: %w", err)
	}
	s.log.Info().Msg("serving " + busName)
	reg := newRegistry()
	sent := false
	report := func() {
		if h := reg.held(); h != sent {
			select {
			case r.Held <- h:
				sent = h
			case <-ctx.Done():
			}
		}
	}
	defer func() {
		if sent && ctx.Err() == nil {
			// The bus went, the session goes on: nothing it held keeps it awake.
			select {
			case r.Held <- false:
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
				// It left before its call reached us (its signal came first),
				// or the bus could not tell: drop it rather than hold the
				// session awake for good.
				s.log.Debug().Str("sender", req.sender).Uint32("cookie", cookie).Msg("inhibitor not on the bus; dropped")
				reg.drop(req.sender)
			} else {
				s.log.Debug().Str("sender", req.sender).Str("app", req.app).Str("reason", req.reason).Uint32("cookie", cookie).Msg("inhibit")
			}
			report()
		}
	}
}

// onBus reports whether a unique connection name still has an owner. Its
// departure signal may already be handled, so an unanswered query is tried
// once more, then counts as gone: a lost inhibition is better than one that
// keeps the session awake until the compositor restarts.
func (s *Service) onBus(ctx context.Context, name string) bool {
	for range 2 {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		var has bool
		err := s.conn.BusObject().CallWithContext(callCtx, "org.freedesktop.DBus.NameHasOwner", 0, name).Store(&has)
		cancel()
		if err == nil {
			return has
		}
		s.log.Debug().Err(err).Str("sender", name).Msg("NameHasOwner failed")
		if ctx.Err() != nil {
			break
		}
	}
	return false
}

// left reports a unique connection name that lost its owner, from the
// bus's own NameOwnerChanged only.
func left(sig *dbus.Signal) (string, bool) {
	if sig.Sender != busDaemon || sig.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(sig.Body) != 3 {
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
type handler struct {
	s        *Service
	activity chan<- struct{}
}

func (h handler) Inhibit(sender dbus.Sender, app, reason string) (uint32, *dbus.Error) {
	r := h.s.call(request{sender: string(sender), app: app, reason: reason})
	return r.cookie, r.err
}

func (h handler) UnInhibit(sender dbus.Sender, cookie uint32) *dbus.Error {
	return h.s.call(request{sender: string(sender), cookie: cookie, unhold: true}).err
}

// GetActive: there is no screensaver to be active, only idle inhibition.
func (handler) GetActive() (bool, *dbus.Error) { return false, nil }

// GetActiveTime is the seconds the screensaver has been active: never.
func (handler) GetActiveTime() (uint32, *dbus.Error) { return 0, nil }

// SetActive cannot start a screensaver there is none of: it reports
// failure, as the specification allows.
func (handler) SetActive(bool) (bool, *dbus.Error) { return false, nil }

// SimulateUserActivity counts as input: idle notifications resume and
// their timers restart. Calls between two reads collapse into one.
func (h handler) SimulateUserActivity() *dbus.Error {
	select {
	case h.activity <- struct{}{}:
	default:
	}
	return nil
}
