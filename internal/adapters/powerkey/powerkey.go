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
	"github.com/bnema/zerobus"
	"github.com/bnema/zerowrap"
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
// again after retry, doubling up to a minute. Meanwhile the old lock stays
// open: logind keeps an inhibitor across its own restart for as long as a
// client holds the descriptor, so the keys stay blocked in the gap, and the
// old lock is closed only once a new one is in place.
func Serve(ctx context.Context, address string, retry time.Duration, log zerowrap.Logger) {
	var lock *os.File
	defer func() {
		if lock != nil {
			_ = lock.Close()
		}
	}()
	busretry.Run(ctx, retry, "logind power key lock", log, func(ctx context.Context) error {
		return hold(ctx, address, log, &lock)
	})
}

// hold takes the locks, replaces *lock with them, and waits until ctx ends
// (nil) or the bus or logind goes (an error).
func hold(ctx context.Context, address string, log zerowrap.Logger, lock **os.File) error {
	conn, err := connect(ctx, address)
	if err != nil {
		return err
	}
	return holdBus(ctx, conn, log, lock)
}

// holdBus owns all reads and encoding; the watcher only closes the connection.
func holdBus(ctx context.Context, conn bus, log zerowrap.Logger, lock **os.File) (err error) {
	stopped, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopped:
		}
	}()
	defer func() {
		close(stopped)
		<-joined
		_ = conn.Close()
		if ctx.Err() != nil {
			err = nil
		}
	}()
	if !conn.UnixFDs() {
		return errors.New("bus did not negotiate Unix fd passing")
	}
	if err := conn.AddMatch("type='signal',sender='org.freedesktop.DBus',interface='org.freedesktop.DBus',member='NameOwnerChanged',arg0='org.freedesktop.login1'"); err != nil {
		return fmt.Errorf("watch %s: %w", logind, err)
	}
	// Closing interrupts a blocked call, including a partial reply. Join the
	// timeout watcher before entering the indefinite signal read loop.
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	callDone, callJoined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(callJoined)
		select {
		case <-callCtx.Done():
			_ = conn.Close()
		case <-callDone:
		}
	}()
	e, serial := conn.NewMethodCall(logind, logindPath, manager, "Inhibit", "ssss")
	e.Str(what)
	e.Str("neferwl")
	e.Str("power keys are bound in the compositor")
	e.Str("block")
	_, callErr := conn.Send()
	var m *zerobus.Message
	for callErr == nil {
		m, callErr = conn.ReadMessage()
		if callErr != nil {
			break
		}
		if lost(m) {
			callErr = errors.New(logind + " left the bus")
			break
		}
		if m.ReplySerial == serial && (m.Type == zerobus.TypeMethodReturn || m.Type == zerobus.TypeError) {
			if m.Type == zerobus.TypeError {
				callErr = fmt.Errorf("inhibit: %s", m.ErrorName)
			}
			break
		}
	}
	close(callDone)
	<-callJoined
	cancel()
	if callErr != nil {
		return fmt.Errorf("inhibit %s: %w", what, callErr)
	}
	if m.Signature != "h" || m.NumFDs() != 1 {
		return errors.New("invalid Inhibit fd reply")
	}
	r := m.Body()
	index := r.UnixFD()
	if r.Err() != nil {
		return fmt.Errorf("inhibit fd: %w", r.Err())
	}
	file, err := m.TakeFD(index)
	if err != nil {
		return fmt.Errorf("inhibit fd: %w", err)
	}
	old := *lock
	*lock = file
	if old != nil {
		_ = old.Close()
	}
	log.Info().Str("what", what).Msg("holding logind key locks")
	for {
		m, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if lost(m) {
			return errors.New(logind + " left the bus")
		}
	}
}

func connect(ctx context.Context, address string) (*zerobus.Conn, error) {
	var conn *zerobus.Conn
	var err error
	if address == "" {
		conn, err = zerobus.SystemBusContext(ctx)
	} else {
		conn, err = zerobus.DialContext(ctx, address)
	}
	if err != nil {
		return nil, fmt.Errorf("bus: %w", err)
	}
	return conn, nil
}

func lost(m *zerobus.Message) bool {
	if m.Type != zerobus.TypeSignal || m.Sender != busDaemon || m.Interface != busDaemon || m.Member != "NameOwnerChanged" || m.Signature != "sss" {
		return false
	}
	r := m.Body()
	name := r.Str()
	_ = r.Str()
	owner := r.Str()
	return r.Err() == nil && name == logind && owner == ""
}
