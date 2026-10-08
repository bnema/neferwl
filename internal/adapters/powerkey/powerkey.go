// Package powerkey takes logind's inhibitor locks for the power, suspend,
// hibernate and reboot keys, so pressing one no longer powers the machine
// off or to sleep by itself: the key reaches the compositor as a key event
// (XF86PowerOff, XF86Sleep, ...) and only a bind acts on it.
package powerkey

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/bnema/neferwl/internal/adapters/busretry"
	"github.com/bnema/zerowrap"
	"github.com/godbus/dbus/v5"
)

const (
	logind     = "org.freedesktop.login1"
	logindPath = "/org/freedesktop/login1"
	manager    = "org.freedesktop.login1.Manager"
	// busDaemon is the sender of the bus's own signals. Any client can send
	// a NameOwnerChanged to us directly, so only this sender is trusted.
	busDaemon = "org.freedesktop.DBus"
	// what lists the logind actions NeferWL takes over.
	what = "handle-power-key:handle-suspend-key:handle-hibernate-key:handle-reboot-key"
	// callTimeout bounds the Inhibit call.
	callTimeout = 3 * time.Second
)

// Serve holds the locks until ctx ends. address is the bus to use, the
// system bus when empty. When the bus or logind goes, it takes the locks
// again after retry, doubling up to busretry.Max: logind drops a lock with
// its own restart, and then handles the keys itself until we ask again.
func Serve(ctx context.Context, address string, retry time.Duration, log zerowrap.Logger) {
	busretry.Run(ctx, retry, "logind power key lock", log, func(ctx context.Context) error {
		return hold(ctx, address, log)
	})
}

// hold takes the locks and keeps them until ctx ends (nil) or the bus or
// logind goes (an error).
func hold(ctx context.Context, address string, log zerowrap.Logger) error {
	conn, err := connect(ctx, address)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)
	// Ask for logind's departure before the lock exists: a restart between
	// the two would go unseen.
	if err := conn.AddMatchSignalContext(ctx, dbus.WithMatchSender(busDaemon), dbus.WithMatchInterface(busDaemon), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, logind)); err != nil {
		return fmt.Errorf("watch %s: %w", logind, err)
	}
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var fd dbus.UnixFD
	if err := conn.Object(logind, logindPath).CallWithContext(callCtx, manager+".Inhibit", 0, what, "neferwl", "power keys are bound in the compositor", "block").Store(&fd); err != nil {
		return fmt.Errorf("inhibit %s: %w", what, err)
	}
	// logind releases the lock when this descriptor closes.
	lock := os.NewFile(uintptr(fd), "logind-inhibit")
	defer func() { _ = lock.Close() }()
	log.Info().Str("what", what).Msg("holding logind key locks")
	for {
		select {
		case <-ctx.Done():
			return nil
		case sig, ok := <-signals:
			if !ok {
				if ctx.Err() != nil {
					return nil // WithContext closed the connection
				}
				return errors.New("bus connection closed")
			}
			if lost(sig) {
				return errors.New(logind + " left the bus")
			}
		}
	}
}

func connect(ctx context.Context, address string) (*dbus.Conn, error) {
	var conn *dbus.Conn
	var err error
	if address == "" {
		conn, err = dbus.SystemBusPrivate(dbus.WithContext(ctx))
	} else {
		conn, err = dbus.Dial(address, dbus.WithContext(ctx))
	}
	if err != nil {
		return nil, fmt.Errorf("system bus: %w", err)
	}
	if err = conn.Auth(nil); err == nil {
		err = conn.Hello()
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("system bus: %w", err)
	}
	return conn, nil
}

// lost reports whether sig is the bus saying logind has no owner any more.
func lost(sig *dbus.Signal) bool {
	if sig.Sender != busDaemon || sig.Name != busDaemon+".NameOwnerChanged" || len(sig.Body) != 3 {
		return false
	}
	name, ok1 := sig.Body[0].(string)
	owner, ok2 := sig.Body[2].(string)
	return ok1 && ok2 && name == logind && owner == ""
}
