package atspi

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bnema/zerobus"
	"golang.org/x/sys/unix"
)

// D-Bus names, paths and interfaces.
const (
	busDaemon     = "org.freedesktop.DBus"
	busDaemonPath = "/org/freedesktop/DBus"
	ifaceProps    = "org.freedesktop.DBus.Properties"

	a11yBusName  = "org.a11y.Bus"
	a11yBusPath  = "/org/a11y/bus"
	a11yStatus   = "org.a11y.Status"
	registryName = "org.a11y.atspi.Registry"
	registryRoot = "/org/a11y/atspi/accessible/root"

	ifaceAccessible = "org.a11y.atspi.Accessible"
	ifaceComponent  = "org.a11y.atspi.Component"
	ifaceCollection = "org.a11y.atspi.Collection"

	// coordWindow is ATSPI_COORD_TYPE_WINDOW: extents relative to the
	// top-level window.
	coordWindow = 1

	// maxInflight bounds the calls sent before a reply is read, so the bus
	// never waits on a full socket while this side waits on it.
	maxInflight = 64
	// maxRefs bounds the objects read from one reply.
	maxRefs = 4096
)

// op is a call the source makes.
type op uint8

const (
	opChildren op = iota // Accessible.GetChildren -> refs
	opPID                // GetConnectionUnixProcessID(name) -> u
	opRole               // Accessible.GetRole -> u
	opState              // Accessible.GetState -> state
	opExtents            // Component.GetExtents(arg) -> rect
	opMatches            // Collection.GetMatches(rule) -> refs
)

// ref is an accessible object: a bus name and a path.
type ref struct{ name, path string }

// call is one request of a batch. name and path address the object; for
// opPID, name is the bus name asked about.
type call struct {
	op         op
	name, path string
	arg        uint32
	rule       matchRule
}

// reply answers the call at the same index of the batch.
type reply struct {
	err   error
	refs  []ref
	u     uint32
	state uint64
	rect  [4]int32 // x, y, w, h
}

// transport sends batches of calls on one bus connection. It is used by one
// goroutine at a time.
type transport interface {
	// Do sends calls pipelined and returns one reply per call, in order.
	// The batch ends at deadline. A call not answered idle after it was
	// sent gets ErrTimeout. When the connection fails or the deadline
	// finds calls unanswered, those calls get an error wrapping
	// ErrBroken: the connection must be dropped.
	Do(deadline time.Time, idle time.Duration, calls []call) []reply
	Close() error
}

// ErrTimeout is the error of a call not answered in time.
var ErrTimeout = errors.New("atspi: call timed out")

// busError is a D-Bus error reply.
type busError struct{ name, msg string }

func (e *busError) Error() string {
	if e.msg == "" {
		return e.name
	}
	return e.name + ": " + e.msg
}

// conn is a transport on a zerobus connection.
type conn struct {
	c   *zerobus.Conn
	raw syscall.RawConn
}

func newConn(c *zerobus.Conn) (*conn, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	return &conn{c: c, raw: raw}, nil
}

func (b *conn) Close() error { return b.c.Close() }

// readable waits until a message can be read or d passes.
func (b *conn) readable(d time.Duration) (bool, error) {
	if b.c.Buffered() {
		return true, nil
	}
	ms := int((d + time.Millisecond - 1) / time.Millisecond)
	if ms < 1 {
		ms = 1
	}
	var n int
	var perr error
	if err := b.raw.Control(func(fd uintptr) {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		for {
			n, perr = unix.Poll(fds, ms)
			if perr != unix.EINTR {
				return
			}
		}
	}); err != nil {
		return false, err
	}
	return n > 0, perr
}

// ErrBroken is the error of calls on a connection that failed or was closed
// at the deadline; the owner must drop it and connect again.
var ErrBroken = errors.New("atspi: connection broken")

// errDeadline is the error of calls still unanswered at the batch deadline:
// late replies and half-read messages leave the connection unusable.
var errDeadline = fmt.Errorf("%w: %w", ErrBroken, ErrTimeout)

// watchdogGrace is how long after the deadline the watchdog closes a socket
// that the batch loop could not leave by itself (a stuck read or write).
const watchdogGrace = 20 * time.Millisecond

// Do sends the calls pipelined. A watchdog closes the socket shortly after
// deadline, so neither a blocked write nor a read stuck in the middle of a
// message can outlive the batch; the connection is then broken. Each call
// has its own idle timeout, counted from its send: traffic for other calls
// does not extend it.
func (b *conn) Do(deadline time.Time, idle time.Duration, calls []call) []reply {
	out := make([]reply, len(calls))
	answered := make([]bool, len(calls))
	type inflight struct {
		idx  int
		sent time.Time
	}
	pending := make(map[uint32]inflight, min(len(calls), maxInflight))
	next, done := 0, 0
	var fired atomic.Bool
	watchdog := time.AfterFunc(time.Until(deadline)+watchdogGrace, func() {
		fired.Store(true)
		_ = b.c.Close()
	})
	defer watchdog.Stop()
	// finish gives err to every call without an answer.
	finish := func(err error) []reply {
		if !errors.Is(err, ErrBroken) && (fired.Load() || !errors.Is(err, ErrTimeout)) {
			err = fmt.Errorf("%w: %w", ErrBroken, err)
		}
		if errors.Is(err, ErrBroken) {
			_ = b.c.Close() // late replies would desynchronize the next batch
		}
		for i := range out {
			if !answered[i] {
				out[i].err = err
			}
		}
		return out
	}
	for done < len(calls) {
		for next < len(calls) && len(pending) < maxInflight {
			if !time.Now().Before(deadline) {
				return finish(errDeadline)
			}
			serial, err := b.send(calls[next])
			if err != nil {
				return finish(err)
			}
			pending[serial] = inflight{idx: next, sent: time.Now()}
			next++
		}
		// Calls that waited longer than idle get no reply.
		now := time.Now()
		var oldest time.Time
		for serial, p := range pending {
			if now.Sub(p.sent) >= idle {
				out[p.idx].err = ErrTimeout
				answered[p.idx] = true
				done++
				delete(pending, serial)
			} else if oldest.IsZero() || p.sent.Before(oldest) {
				oldest = p.sent
			}
		}
		if len(pending) == 0 {
			continue
		}
		wait := min(time.Until(oldest.Add(idle)), time.Until(deadline))
		if wait <= 0 {
			if !time.Now().Before(deadline) {
				break
			}
			continue
		}
		ok, err := b.readable(wait)
		if err != nil {
			return finish(err)
		}
		if !ok {
			continue
		}
		m, err := b.c.ReadMessage()
		if err != nil {
			return finish(err)
		}
		p, found := pending[m.ReplySerial]
		if !found || (m.Type != zerobus.TypeMethodReturn && m.Type != zerobus.TypeError) {
			// A signal, or the late reply of an earlier batch.
			continue
		}
		delete(pending, m.ReplySerial)
		decode(m, calls[p.idx], &out[p.idx])
		answered[p.idx] = true
		done++
	}
	if done == len(calls) {
		return out
	}
	return finish(errDeadline)
}

// send builds and sends one call and returns its serial.
func (b *conn) send(c call) (uint32, error) {
	switch c.op {
	case opChildren:
		b.c.NewCall(c.name, c.path, ifaceAccessible, "GetChildren", "")
	case opPID:
		b.c.NewCall(busDaemon, busDaemonPath, busDaemon, "GetConnectionUnixProcessID", "s").Str(c.name)
	case opRole:
		b.c.NewCall(c.name, c.path, ifaceAccessible, "GetRole", "")
	case opState:
		b.c.NewCall(c.name, c.path, ifaceAccessible, "GetState", "")
	case opExtents:
		b.c.NewCall(c.name, c.path, ifaceComponent, "GetExtents", "u").Uint32(c.arg)
	case opMatches:
		e := b.c.NewCall(c.name, c.path, ifaceCollection, "GetMatches", "(aiia{ss}iaiiasib)uib")
		encodeRule(e, c.rule)
		e.Uint32(sortCanonical)
		e.Int32(matchCount)
		e.Bool(true)
	}
	return b.c.Send()
}

// encodeRule writes the MatchRule struct (aiia{ss}iaiiasib): states, state
// match, attributes, attribute match, roles, role match, interfaces,
// interface match, invert.
func encodeRule(e *zerobus.Encoder, r matchRule) {
	e.Struct()
	a := e.BeginArray('i')
	for _, w := range stateBits(stateShowing, stateVisible) {
		e.Int32(w)
	}
	e.EndArray(a)
	e.Int32(matchAll)
	m := e.BeginArray('{')
	e.EndArray(m)
	e.Int32(matchAll)
	a = e.BeginArray('i')
	for _, w := range roleBits(r.roles()) {
		e.Int32(w)
	}
	e.EndArray(a)
	e.Int32(matchAny)
	s := e.BeginArray('s')
	e.EndArray(s)
	e.Int32(matchAll)
	e.Bool(false)
}

// decode reads the reply m of c into out.
func decode(m *zerobus.Message, c call, out *reply) {
	if m.Type == zerobus.TypeError {
		be := &busError{name: strings.Clone(m.ErrorName)}
		if strings.HasPrefix(m.Signature, "s") {
			be.msg = strings.Clone(m.Body().Str())
		}
		out.err = be
		return
	}
	r := m.Body()
	want := func(sig string) bool {
		if m.Signature != sig {
			out.err = fmt.Errorf("atspi: reply signature %q, want %q", m.Signature, sig)
			return false
		}
		return true
	}
	switch c.op {
	case opChildren, opMatches:
		if !want("a(so)") {
			return
		}
		end := r.Array('(')
		for r.More(end) {
			if len(out.refs) >= maxRefs {
				break
			}
			r.Struct()
			name := r.Str()
			path := r.ObjectPath()
			if r.Err() != nil {
				break
			}
			out.refs = append(out.refs, ref{strings.Clone(name), strings.Clone(path)})
		}
	case opPID, opRole:
		if want("u") {
			out.u = r.Uint32()
		}
	case opState:
		if !want("au") {
			return
		}
		end := r.Array('u')
		var words [2]uint32
		for i := 0; r.More(end); i++ {
			w := r.Uint32()
			if i < len(words) {
				words[i] = w
			}
		}
		out.state = uint64(words[0]) | uint64(words[1])<<32
	case opExtents:
		if want("(iiii)") {
			r.Struct()
			out.rect = [4]int32{r.Int32(), r.Int32(), r.Int32(), r.Int32()}
		}
	}
	if out.err == nil && r.Err() != nil {
		out.err = r.Err()
	}
}
