package screencast

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/captureallow"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/zerobus"
	"github.com/stretchr/testify/mock"
)

func testInventory() domain.CastInventory {
	return domain.CastInventory{
		Focused: "HEADLESS-1",
		Outputs: []domain.CastOutput{
			{Info: domain.OutputInfo{Name: "HEADLESS-2", Make: "Acme", Model: "Two", Width: 32, Height: 32, RefreshMilli: 30000}, Modes: []domain.OutputMode{{Width: 32, Height: 32, RefreshMilli: 30000, Preferred: true}}, Current: 0, X: 64, Width: 32, Height: 32, Scale: 1},
			{Info: domain.OutputInfo{Name: "HEADLESS-1", Width: 64, Height: 32, RefreshMilli: 60000}, Modes: []domain.OutputMode{{Width: 64, Height: 32, RefreshMilli: 60000, Preferred: true}, {Width: 32, Height: 16, RefreshMilli: 60000}}, Current: 0, Width: 64, Height: 32, Scale: 1},
		},
		Windows: []domain.CastWindow{{ID: 7, AppID: "org.example.App", Title: "Hello"}, {ID: 3, AppID: "foot", Title: "sh"}},
	}
}

// ready starts the service, waits for its names and publishes the inventory.
func ready(t *testing.T, o harnessOpts) (*svcHarness, *zerobus.Conn) {
	t.Helper()
	h := newSvcHarness(t, o)
	c := h.client()
	for _, n := range []string{nameDisplayConfig, nameIntrospect, nameScreenshot} {
		h.waitName(c, n)
	}
	if !o.noPW {
		h.waitName(c, nameScreenCast)
	}
	h.svc.Inventory(testInventory())
	// The owner signals once it applied the inventory.
	receive(t, h.applied)
	return h, c
}

func createSession(t *testing.T, c *zerobus.Conn) string {
	t.Helper()
	name, m := callErr(t, c, nameScreenCast, pathScreenCast, ifaceScreenCast, "CreateSession", "a{sv}", func(e *zerobus.Encoder) {
		a := e.BeginArray('{')
		e.EndArray(a)
	})
	if name != "" {
		t.Fatalf("CreateSession: %s", name)
	}
	return strings.Clone(m.Body().ObjectPath())
}

func recordMonitor(t *testing.T, c *zerobus.Conn, sess, connector string) (string, string) {
	name, m := callErr(t, c, nameScreenCast, sess, ifaceSession, "RecordMonitor", "sa{sv}", func(e *zerobus.Encoder) {
		e.Str(connector)
		a := e.BeginArray('{')
		e.Struct()
		e.Str("cursor-mode")
		e.Variant("u")
		e.Uint32(1)
		e.EndArray(a)
	})
	if name != "" {
		return name, ""
	}
	return "", strings.Clone(m.Body().ObjectPath())
}

// Every method of the contract answers a caller on the allowlist.
func TestMethodMatrix(t *testing.T) {
	h, c := ready(t, harnessOpts{})
	_ = h
	sess := createSession(t, c)
	if sess != "/org/gnome/Mutter/ScreenCast/Session/u1" {
		t.Fatalf("session path %q", sess)
	}

	// Properties of the root object.
	name, m := callErr(t, c, nameScreenCast, pathScreenCast, ifaceProps, "Get", "ss", func(e *zerobus.Encoder) { e.Str(ifaceScreenCast); e.Str("Version") })
	if name != "" || m.Signature != "v" {
		t.Fatalf("Get Version: %s %v", name, m)
	}
	r := m.Body()
	if sig := r.Variant(); sig != "i" || r.Int32() != 4 {
		t.Fatal("Version is not 4")
	}
	if name, _ = callErr(t, c, nameScreenCast, pathScreenCast, ifaceProps, "Get", "ss", func(e *zerobus.Encoder) { e.Str(ifaceScreenCast); e.Str("Nope") }); name != errUnknownProperty {
		t.Fatalf("unknown property: %q", name)
	}
	if name, _ = callErr(t, c, nameScreenCast, pathScreenCast, ifaceProps, "Set", "ssv", func(e *zerobus.Encoder) {
		e.Str(ifaceScreenCast)
		e.Str("Version")
		e.Variant("i")
		e.Int32(1)
	}); name != errPropertyReadOnly {
		t.Fatalf("Set: %q", name)
	}
	name, m = callErr(t, c, nameScreenCast, pathScreenCast, ifaceProps, "GetAll", "s", func(e *zerobus.Encoder) { e.Str(ifaceScreenCast) })
	if name != "" || m.Signature != "a{sv}" {
		t.Fatalf("GetAll: %s", name)
	}

	// Streams: the monitor, the window, and what is not supported.
	stream, got := "", ""
	if name, stream = recordMonitor(t, c, sess, "HEADLESS-1"); name != "" {
		t.Fatalf("RecordMonitor: %s", name)
	}
	if stream != "/org/gnome/Mutter/ScreenCast/Stream/u1" {
		t.Fatalf("stream path %q", stream)
	}
	if name, _ = recordMonitor(t, c, sess, "NOPE-1"); name != errInvalidArgs {
		t.Fatalf("RecordMonitor of a missing monitor: %q", name)
	}
	name, m = callErr(t, c, nameScreenCast, sess, ifaceSession, "RecordWindow", "a{sv}", func(e *zerobus.Encoder) {
		a := e.BeginArray('{')
		e.Struct()
		e.Str("window-id")
		e.Variant("t")
		e.Uint64(7)
		e.EndArray(a)
	})
	if name != "" {
		t.Fatalf("RecordWindow: %s", name)
	}
	got = strings.Clone(m.Body().ObjectPath())
	if got != "/org/gnome/Mutter/ScreenCast/Stream/u2" {
		t.Fatalf("window stream path %q", got)
	}
	name, _ = callErr(t, c, nameScreenCast, sess, ifaceSession, "RecordWindow", "a{sv}", func(e *zerobus.Encoder) {
		a := e.BeginArray('{')
		e.Struct()
		e.Str("window-id")
		e.Variant("t")
		e.Uint64(99)
		e.EndArray(a)
	})
	if name != errInvalidArgs {
		t.Fatalf("RecordWindow of a closed window: %q", name)
	}
	if name, _ = callErr(t, c, nameScreenCast, sess, ifaceSession, "RecordArea", "iiiia{sv}", func(e *zerobus.Encoder) {
		e.Int32(0)
		e.Int32(0)
		e.Int32(1)
		e.Int32(1)
		e.EndArray(e.BeginArray('{'))
	}); name == "" {
		t.Fatal("RecordArea is not supported")
	}

	// The stream answers GetAll with its parameters before Start.
	name, m = callErr(t, c, nameScreenCast, stream, ifaceProps, "GetAll", "s", func(e *zerobus.Encoder) { e.Str(ifaceStream) })
	if name != "" {
		t.Fatalf("stream GetAll: %s", name)
	}
	r = m.Body()
	end := r.Array('{')
	r.Struct()
	if r.Str() != "Parameters" || r.Variant() != "a{sv}" {
		t.Fatal("no Parameters")
	}
	pend := r.Array('{')
	seen := map[string][2]int32{}
	for r.More(pend) {
		r.Struct()
		key := r.Str()
		if sig := r.Variant(); sig != "(ii)" {
			t.Fatalf("%s is %s", key, sig)
		}
		r.Struct()
		seen[key] = [2]int32{r.Int32(), r.Int32()}
	}
	_ = end
	if seen["position"] != [2]int32{0, 0} || seen["size"] != [2]int32{64, 32} {
		t.Fatalf("parameters %v", seen)
	}

	// Introspect and DisplayConfig properties.
	name, m = callErr(t, c, nameIntrospect, pathIntrospect, ifaceProps, "Get", "ss", func(e *zerobus.Encoder) { e.Str(ifaceIntrospect); e.Str("version") })
	if name != "" {
		t.Fatalf("version: %s", name)
	}
	r = m.Body()
	if r.Variant() != "u" || r.Uint32() != 1 {
		t.Fatal("Introspect version is not 1")
	}
	if name, _ = callErr(t, c, nameDisplayConfig, pathDisplayConfig, ifaceDisplayConfig, "ApplyMonitorsConfig", "", nil); name == "" {
		t.Fatal("ApplyMonitorsConfig is not supported")
	}
	if name, _ = callErr(t, c, nameScreenCast, "/org/gnome/Mutter/ScreenCast/Session/u99", ifaceSession, "Stop", "", nil); name != errUnknownObject {
		t.Fatalf("unknown session: %q", name)
	}
	if name, _ = callErr(t, c, nameScreenCast, sess, ifaceSession, "Stop", "", nil); name != "" {
		t.Fatalf("Stop: %q", name)
	}
	if name, _ = callErr(t, c, nameScreenCast, sess, ifaceSession, "Stop", "", nil); name != errUnknownObject {
		t.Fatalf("Stop twice: %q", name)
	}
}

// A signal from the session reaches only its owner: Stop is answered, then
// the owner hears Closed.
func TestStopSignalsClosedToOwner(t *testing.T) {
	h, c := ready(t, harnessOpts{})
	other := h.client()
	rule := "type='signal',interface='" + ifaceSession + "'"
	for _, x := range []*zerobus.Conn{c, other} {
		checkMatch := boundIO(t, "AddMatch", func() { _ = x.Close() })
		defer checkMatch()
		if err := x.AddMatch(rule); err != nil {
			t.Fatal(err)
		}
	}
	sess := createSession(t, c)
	if name, _ := callErr(t, c, nameScreenCast, sess, ifaceSession, "Stop", "", nil); name != "" {
		t.Fatal(name)
	}
	m := readUntil(t, c, "Closed")
	if m.Path != sess || m.Type != zerobus.TypeSignal {
		t.Fatalf("closed %+v", m)
	}
	// The other client would have heard it before the answer of this call.
	other.Handle = func(m *zerobus.Message) {
		if m.Member == "Closed" {
			t.Error("Closed reached a client that is not the owner")
		}
	}
	other.NewCall("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "GetId", "")
	checkCall := boundIO(t, "bus call", func() { _ = other.Close() })
	defer checkCall()
	if _, err := other.Call(); err != nil {
		t.Fatal(err)
	}
}

func readUntil(t *testing.T, c *zerobus.Conn, member string) *zerobus.Message {
	t.Helper()
	check := boundIO(t, "signal "+member, func() { _ = c.Close() })
	defer check()
	for {
		m, err := c.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if m.Member == member {
			return m
		}
	}
}

// A caller whose executable is not on the allowlist is refused by every
// object and every method, and nothing is created.
func TestDisallowedCallerGetsAccessDenied(t *testing.T) {
	h, c := ready(t, harnessOpts{})
	h.peers.exe.Store("/usr/bin/evil")
	for _, tc := range []struct {
		dest, path, iface, member, sig string
		body                           func(*zerobus.Encoder)
	}{
		{nameScreenCast, pathScreenCast, ifaceScreenCast, "CreateSession", "a{sv}", func(e *zerobus.Encoder) { e.EndArray(e.BeginArray('{')) }},
		{nameScreenCast, pathScreenCast, ifaceProps, "GetAll", "s", func(e *zerobus.Encoder) { e.Str(ifaceScreenCast) }},
		{nameScreenCast, pathScreenCast, ifaceProps, "Get", "ss", func(e *zerobus.Encoder) { e.Str(ifaceScreenCast); e.Str("Version") }},
		{nameDisplayConfig, pathDisplayConfig, ifaceDisplayConfig, "GetCurrentState", "", nil},
		{nameIntrospect, pathIntrospect, ifaceIntrospect, "GetWindows", "", nil},
		{nameScreenshot, pathScreenshot, ifaceScreenshot, "InteractiveScreenshot", "", nil},
		{nameScreenshot, pathScreenshot, ifaceScreenshot, "Screenshot", "bbs", func(e *zerobus.Encoder) { e.Bool(false); e.Bool(false); e.Str("x") }},
		{nameIntrospect, pathIntrospect, ifacePeer, "Ping", "", nil},
	} {
		if name, _ := callErr(t, c, tc.dest, tc.path, tc.iface, tc.member, tc.sig, tc.body); name != errAccessDenied {
			t.Errorf("%s.%s: %q", tc.iface, tc.member, name)
		}
	}
	select {
	case cmd := <-h.cmds:
		t.Fatalf("command sent for a refused caller: %+v", cmd)
	default:
	}
}

// A policy that closes is applied at once: the cache keeps the executable,
// not the verdict.
func TestPolicyChangeApplies(t *testing.T) {
	h := newSvcHarness(t, harnessOpts{allow: captureallow.Closed()})
	c := h.client()
	h.waitName(c, nameIntrospect)
	if name, _ := callErr(t, c, nameIntrospect, pathIntrospect, ifaceIntrospect, "GetWindows", "", nil); name != errAccessDenied {
		t.Fatalf("closed policy: %q", name)
	}
}

// The executable is read again where it matters: an exec after the first
// check is caught at CreateSession.
func TestExecAfterFirstCheckIsCaught(t *testing.T) {
	h, c := ready(t, harnessOpts{})
	if name, _ := callErr(t, c, nameIntrospect, pathIntrospect, ifaceIntrospect, "GetWindows", "", nil); name != "" {
		t.Fatalf("first call: %q", name)
	}
	h.peers.exe.Store("/usr/bin/evil")
	// Not a privileged entry point: the cached executable still serves it.
	if name, _ := callErr(t, c, nameDisplayConfig, pathDisplayConfig, ifaceDisplayConfig, "GetCurrentState", "", nil); name != "" {
		t.Fatalf("cached call: %q", name)
	}
	name, _ := callErr(t, c, nameScreenCast, pathScreenCast, ifaceScreenCast, "CreateSession", "a{sv}", func(e *zerobus.Encoder) { e.EndArray(e.BeginArray('{')) })
	if name != errAccessDenied {
		t.Fatalf("CreateSession after exec: %q", name)
	}
}

// A pidfd that names another process than the one the bus named is refused.
func TestPidMismatchIsRefused(t *testing.T) {
	h := newSvcHarness(t, harnessOpts{})
	h.peers.exePid = func(open int) int { return open + 1 }
	c := h.client()
	h.waitName(c, nameIntrospect)
	if name, _ := callErr(t, c, nameIntrospect, pathIntrospect, ifaceIntrospect, "GetWindows", "", nil); name != errAccessDenied {
		t.Fatalf("pid mismatch: %q", name)
	}
}

func TestPidfdOpenFailureIsRefused(t *testing.T) {
	h := newSvcHarness(t, harnessOpts{})
	h.peers.openErr = errors.New("no such process")
	c := h.client()
	h.waitName(c, nameIntrospect)
	if name, _ := callErr(t, c, nameIntrospect, pathIntrospect, ifaceIntrospect, "GetWindows", "", nil); name != errAccessDenied {
		t.Fatalf("pidfd_open failure: %q", name)
	}
}

// The session and its streams answer only the unique name that created it.
func TestNonOwnerIsDenied(t *testing.T) {
	h, c := ready(t, harnessOpts{})
	other := h.client()
	sess := createSession(t, c)
	_, stream := recordMonitor(t, c, sess, "HEADLESS-1")
	for _, tc := range []struct{ path, iface, member, sig string }{
		{sess, ifaceSession, "Stop", ""},
		{sess, ifaceSession, "Start", ""},
		{sess, ifaceProps, "GetAll", "s"},
		{stream, ifaceProps, "GetAll", "s"},
		{stream, ifaceProps, "Get", "ss"},
	} {
		name, _ := callErr(t, other, nameScreenCast, tc.path, tc.iface, tc.member, tc.sig, func(e *zerobus.Encoder) {
			switch tc.sig {
			case "s":
				e.Str(ifaceSession)
			case "ss":
				e.Str(ifaceStream)
				e.Str("Parameters")
			}
		})
		if name != errAccessDenied {
			t.Errorf("%s %s: %q", tc.path, tc.member, name)
		}
	}
	if name, _ := recordMonitor(t, other, sess, "HEADLESS-1"); name != errAccessDenied {
		t.Errorf("RecordMonitor by a non-owner: %q", name)
	}
	// The owner is unaffected.
	if name, _ := callErr(t, c, nameScreenCast, sess, ifaceSession, "Stop", "", nil); name != "" {
		t.Fatalf("owner Stop: %q", name)
	}
}

// Requests wait in a fixed table while their sender is checked: when it is
// full, the next one gets LimitsExceeded, and the parked ones are answered
// once the check ends.
//
//testpolicy:realtime service integration uses a private D-Bus daemon and Unix descriptors
func TestPendingTableFull(t *testing.T) {
	// Drive admission on this goroutine, delaying the caller-check transition
	// rather than blocking Open on the service owner. A PID reply may arrive
	// before all calls do; blocking Open then prevents the owner filling the
	// table or answering the overflow at all.
	addr, _ := startBus(t)
	c, err := zerobus.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	server, err := zerobus.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	s := New(captureallow.Builtin, nil, testLog(&logBuf{}))
	s.conn = server
	peer := &s.callers[0]
	*peer = caller{state: callerPid, name: c.UniqueName()}
	for i := range maxPending + 1 {
		s.admit(&request{sender: c.UniqueName(), serial: uint32(i + 1), obj: objIntrospect, method: mGetWindows})
	}
	// Closing the real connection bounds reads without concurrently using its
	// encoder/decoder. No reply means a failed test, not the CI-wide timeout.
	timer := time.AfterFunc(5*time.Second, func() { _ = c.Close() })
	defer timer.Stop()
	m, err := c.ReadMessage()
	for err == nil && m.Type == zerobus.TypeSignal {
		m, err = c.ReadMessage()
	}
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != zerobus.TypeError || m.ReplySerial != maxPending+1 || m.ErrorName != errLimits {
		t.Fatalf("17th request: type %d error %q reply %d", m.Type, m.ErrorName, m.ReplySerial)
	}
	peer.state, peer.exe = callerKnown, "/usr/bin/grim"
	s.flush(peer)
	for range maxPending {
		m, err := c.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if m.Type != zerobus.TypeMethodReturn {
			t.Fatalf("parked request: type %d %q", m.Type, m.ErrorName)
		}
	}
}

// Closing the owner's connection stops its session: its slot is freed, so
// a new session starts again from the table.
func TestOwnerVanishFreesSessions(t *testing.T) {
	h, c := ready(t, harnessOpts{})
	owner := h.client()
	for range maxSessions {
		createSession(t, owner)
	}
	if name, _ := callErr(t, owner, nameScreenCast, pathScreenCast, ifaceScreenCast, "CreateSession", "a{sv}", func(e *zerobus.Encoder) { e.EndArray(e.BeginArray('{')) }); name != errLimits {
		t.Fatalf("over the limit: %q", name)
	}
	_ = owner.Close()
	// The owner frees the vanished owner's sessions on NameOwnerChanged.
	receive(t, h.reaped)
	if name, _ := callErr(t, c, nameScreenCast, pathScreenCast, ifaceScreenCast, "CreateSession", "a{sv}", func(e *zerobus.Encoder) { e.EndArray(e.BeginArray('{')) }); name != "" {
		t.Fatalf("sessions of a vanished owner stay: %q\n%s", name, h.log.String())
	}
}

// The remote desktop key is refused, and a service without PipeWire does not
// take the ScreenCast name.
func TestRemoteDesktopRefusedAndNoPipeWire(t *testing.T) {
	_, c := ready(t, harnessOpts{})
	name, _ := callErr(t, c, nameScreenCast, pathScreenCast, ifaceScreenCast, "CreateSession", "a{sv}", func(e *zerobus.Encoder) {
		a := e.BeginArray('{')
		e.Struct()
		e.Str("remote-desktop-session-id")
		e.Variant("s")
		e.Str("x")
		e.EndArray(a)
	})
	if name != errNotSupported {
		t.Fatalf("remote desktop: %q", name)
	}

	h := newSvcHarness(t, harnessOpts{noPW: true})
	c2 := h.client()
	h.waitServing(c2)
	e := c2.NewCall("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "NameHasOwner", "s")
	e.Str(nameScreenCast)
	checkCall := boundIO(t, "NameHasOwner", func() { _ = c2.Close() })
	defer checkCall()
	m, err := c2.Call()
	if err != nil || m.Body().Bool() {
		t.Fatalf("ScreenCast name taken without PipeWire (%v)", err)
	}
}

// The encoding of GetCurrentState and GetWindows from an inventory.
func TestDisplayConfigAndWindows(t *testing.T) {
	_, c := ready(t, harnessOpts{})
	name, m := callErr(t, c, nameDisplayConfig, pathDisplayConfig, ifaceDisplayConfig, "GetCurrentState", "", nil)
	if name != "" {
		t.Fatal(name)
	}
	r := m.Body()
	if r.Uint32() != 0 {
		t.Fatal("serial")
	}
	type mon struct {
		connector string
		current   int
		modes     int
		display   string
		builtin   bool
	}
	var mons []mon
	end := r.Array('(')
	for r.More(end) {
		r.Struct()
		r.Struct()
		x := mon{connector: r.Str()}
		r.Str()
		r.Str()
		r.Str()
		mend := r.Array('(')
		for r.More(mend) {
			r.Struct()
			r.Str()
			r.Int32()
			r.Int32()
			r.Float64()
			r.Float64()
			send := r.Array('d')
			for r.More(send) {
				r.Float64()
			}
			pend := r.Array('{')
			x.modes++
			for r.More(pend) {
				r.Struct()
				key := r.Str()
				sig := r.Variant()
				if key == "is-current" && r.Bool() {
					x.current++
				} else if sig == "b" {
					r.Bool()
				}
			}
		}
		pend := r.Array('{')
		for r.More(pend) {
			r.Struct()
			key := r.Str()
			switch r.Variant() {
			case "s":
				if v := r.Str(); key == "display-name" {
					x.display = v
				}
			case "b":
				x.builtin = r.Bool()
			}
		}
		mons = append(mons, x)
	}
	logicals := 0
	lend := r.Array('(')
	for r.More(lend) {
		r.Struct()
		r.Int32()
		r.Int32()
		r.Float64()
		r.Uint32()
		r.Bool()
		mend := r.Array('(')
		for r.More(mend) {
			r.Struct()
			r.Str()
			r.Str()
			r.Str()
			r.Str()
		}
		pend := r.Array('{')
		r.SkipTo(pend)
		logicals++
	}
	pend := r.Array('{')
	r.Struct()
	if r.Str() != "layout-mode" || r.Variant() != "u" || r.Uint32() != 1 {
		t.Fatal("layout-mode")
	}
	_ = pend
	if !r.Done() {
		t.Fatalf("trailing data: %v", r.Err())
	}
	if len(mons) != 2 || logicals != 2 || mons[0].connector != "HEADLESS-1" || mons[1].connector != "HEADLESS-2" {
		t.Fatalf("monitors %+v logical %d", mons, logicals)
	}
	for _, x := range mons {
		if x.current != 1 {
			t.Errorf("%s has %d current modes", x.connector, x.current)
		}
	}
	if mons[0].modes != 2 || mons[1].display != "Acme Two" || mons[0].display != "HEADLESS-1" {
		t.Fatalf("monitors %+v", mons)
	}

	name, m = callErr(t, c, nameIntrospect, pathIntrospect, ifaceIntrospect, "GetWindows", "", nil)
	if name != "" {
		t.Fatal(name)
	}
	r = m.Body()
	got := map[uint64][2]string{}
	wend := r.Array('{')
	for r.More(wend) {
		r.Struct()
		id := r.Uint64()
		var v [2]string
		pend := r.Array('{')
		for r.More(pend) {
			r.Struct()
			key := r.Str()
			r.Variant()
			val := r.Str()
			switch key {
			case "app-id":
				v[0] = val
			case "title":
				v[1] = val
			}
		}
		got[id] = v
	}
	if len(got) != 2 || got[7] != [2]string{"org.example.App", "Hello"} || got[3] != [2]string{"foot", "sh"} {
		t.Fatalf("windows %v", got)
	}
}

// MonitorsChanged and WindowsChanged are directed: a peer that was refused
// gets nothing, even with a match rule for them; an allowed one gets them,
// and a peer that left the bus is forgotten.
//
//testpolicy:realtime service integration uses a private D-Bus daemon and Unix descriptors
func TestInventorySignalsGoToAllowedPeersOnly(t *testing.T) {
	h := newSvcHarness(t, harnessOpts{})
	h.peers.exeFile = func(n int) string {
		if n == 0 || n == 2 {
			return "/usr/bin/grim"
		}
		return "/usr/bin/evil"
	}
	allowed, denied, gone := h.client(), h.client(), h.client()
	for _, n := range []string{nameDisplayConfig, nameIntrospect} {
		h.waitName(allowed, n)
	}
	rule := "type='signal',path='" + pathIntrospect + "'"
	rule2 := "type='signal',path='" + pathDisplayConfig + "'"
	for _, x := range []*zerobus.Conn{allowed, denied, gone} {
		for _, r := range []string{rule, rule2} {
			checkMatch := boundIO(t, "AddMatch", func() { _ = x.Close() })
			defer checkMatch()
			if err := x.AddMatch(r); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Check in order: allowed (pidfd 0), denied (1), gone (2).
	for i, x := range []*zerobus.Conn{allowed, denied, gone} {
		want := ""
		if i == 1 {
			want = errAccessDenied
		}
		if name, _ := callErr(t, x, nameIntrospect, pathIntrospect, ifaceIntrospect, "GetWindows", "", nil); name != want {
			t.Fatalf("peer %d: %q, want %q", i, name, want)
		}
	}
	_ = gone.Close()
	// The first inventory is not a change. Wait until the owner applied it
	// (a published inventory replaces an unread one), then publish a change.
	h.svc.Inventory(testInventory())
	receive(t, h.applied)
	changed := testInventory()
	changed.Windows = append(changed.Windows, domain.CastWindow{ID: 9, AppID: "x", Title: "new"})
	changed.Outputs[0].X = 100
	h.svc.Inventory(changed)
	got := make(chan string, 8)
	go func() {
		for {
			m, err := allowed.ReadMessage()
			if err != nil {
				return
			}
			if m.Type == zerobus.TypeSignal && (m.Member == "WindowsChanged" || m.Member == "MonitorsChanged") {
				select {
				case got <- m.Member:
				case <-time.After(5 * time.Second):
					return
				}
			}
		}
	}()
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case m := <-got:
			seen[m] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("allowed peer heard %v", seen)
		}
	}
	// The service's owner goroutine answers in order: a call that it
	// answers (AccessDenied) comes after any signal it had wrongly directed
	// to this connection. The handler is active during the call.
	denied.Handle = func(m *zerobus.Message) {
		if m.Member == "WindowsChanged" || m.Member == "MonitorsChanged" {
			t.Errorf("refused peer got %s", m.Member)
		}
	}
	if name, _ := callErr(t, denied, nameIntrospect, pathIntrospect, ifaceIntrospect, "GetWindows", "", nil); name != errAccessDenied {
		t.Fatalf("refused peer: %q", name)
	}
}

// pidfdClosed reports whether the file was closed.
func pidfdClosed(f *os.File) bool {
	_, err := f.Stat()
	return errors.Is(err, os.ErrClosed)
}

// Refused callers are not cached: more of them than the cache holds still
// leave room for the allowed peer, and their pidfds are closed at once.
func TestDeniedCallersDoNotExhaustTheCache(t *testing.T) {
	const denied = maxCallers + 8
	// One pidfd per check: the first peer is checked twice.
	const deniedChecks = denied + 1
	h := newSvcHarness(t, harnessOpts{})
	h.peers.exeFile = func(n int) string {
		if n < deniedChecks {
			return "/usr/bin/evil"
		}
		return "/usr/bin/grim"
	}
	c0 := h.client()
	h.waitName(c0, nameIntrospect)
	for i := range denied {
		x := h.client()
		if name, _ := callErr(t, x, nameIntrospect, pathIntrospect, ifaceIntrospect, "GetWindows", "", nil); name != errAccessDenied {
			t.Fatalf("denied peer %d: %q", i, name)
		}
		// The same peer asking again is checked again, never LimitsExceeded.
		if i == 0 {
			if name, _ := callErr(t, x, nameIntrospect, pathIntrospect, ifaceIntrospect, "GetWindows", "", nil); name != errAccessDenied {
				t.Fatalf("denied peer again: %q", name)
			}
		}
	}
	good := h.client()
	if name, _ := callErr(t, good, nameIntrospect, pathIntrospect, ifaceIntrospect, "GetWindows", "", nil); name != "" {
		t.Fatalf("allowed peer after %d denied: %q", denied, name)
	}
	h.peers.mu.Lock()
	opened := slices.Clone(h.peers.opened)
	h.peers.mu.Unlock()
	for i, f := range opened[:deniedChecks] {
		if !pidfdClosed(f) {
			t.Fatalf("pidfd of denied peer %d still open", i)
		}
	}
	if pidfdClosed(opened[len(opened)-1]) {
		t.Fatal("pidfd of the allowed peer closed while it is cached")
	}
}

// The pidfds of the cached callers are closed when the bus is lost.
func TestBusLossClosesCallerPidfds(t *testing.T) {
	h := newSvcHarness(t, harnessOpts{})
	c := h.client()
	h.waitName(c, nameIntrospect)
	if name, _ := callErr(t, c, nameIntrospect, pathIntrospect, ifaceIntrospect, "GetWindows", "", nil); name != "" {
		t.Fatal(name)
	}
	h.peers.mu.Lock()
	opened := slices.Clone(h.peers.opened)
	h.peers.mu.Unlock()
	if len(opened) != 1 || pidfdClosed(opened[0]) {
		t.Fatalf("%d pidfds, want one open", len(opened))
	}
	h.killBus()
	h.peers.waitClosed(t, 0)
	if !pidfdClosed(opened[0]) {
		t.Fatal("pidfd of a cached caller survives the bus loss")
	}
}

// A call triggered by an early name must be dispatched while acquire waits
// for the final RequestName, not consumed by a synchronous Call's nil handler.
func TestCallDuringNameAcquisition(t *testing.T) {
	addr, _ := startBus(t)
	client, err := zerobus.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := zerobus.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	defer boundIO(t, "name acquisition", func() { _ = server.Close(); _ = client.Close() })()
	if err := client.AddMatch("type='signal',interface='org.freedesktop.DBus',member='NameOwnerChanged',arg0='" + nameIntrospect + "'"); err != nil {
		t.Fatal(err)
	}
	s := New(captureallow.Builtin, nil, testLog(&logBuf{}))
	defer s.poller.Close()
	// Authentication is orthogonal; this peer has already been checked.
	s.callers[0] = caller{state: callerKnown, name: client.UniqueName(), exe: "/usr/bin/grim"}
	b := newMockbus(t)
	s.conn = b
	s.ctx = context.Background()
	b.EXPECT().AddMatch(mock.Anything).RunAndReturn(server.AddMatch).Once()
	var serial uint32
	requests := 0
	var queued *zerobus.Message
	b.EXPECT().NewMethodCall(busDaemon, busDaemonPath, busDaemon, "RequestName", "su").RunAndReturn(func(dest, path, iface, member, sig string) (*zerobus.Encoder, uint32) {
		requests++
		if requests == 3 { // ScreenCast is skipped without PipeWire.
			for {
				m, err := client.ReadMessage()
				if err != nil {
					t.Fatal(err)
				}
				if m.Member == "NameOwnerChanged" {
					break
				}
			}
			client.NewCall(nameIntrospect, pathIntrospect, ifaceIntrospect, "GetWindows", "")
			var err error
			serial, err = client.Send()
			if err != nil {
				t.Fatal(err)
			}
			// Receive the routed call before sending the final RequestName. Return
			// it on the next read so dispatch during that wait is deterministic.
			for {
				incoming, err := server.ReadMessage()
				if err != nil {
					t.Fatal(err)
				}
				if incoming.Type == zerobus.TypeMethodCall {
					queued = incoming
					break
				}
			}
		}
		return server.NewMethodCall(dest, path, iface, member, sig)
	}).Times(3)
	b.EXPECT().Send().RunAndReturn(server.Send)
	b.EXPECT().ReadMessage().RunAndReturn(func() (*zerobus.Message, error) {
		if queued != nil {
			m := queued
			queued = nil
			return m, nil
		}
		return server.ReadMessage()
	})
	b.EXPECT().NewReply(mock.Anything, mock.Anything).RunAndReturn(server.NewReply).Once()
	if err := s.acquire(); err != nil {
		t.Fatal(err)
	}
	if !s.owns[1] || !s.owns[2] || !s.owns[3] {
		t.Fatalf("owned names: %v", s.owns)
	}
	for {
		m, err := client.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if m.ReplySerial != serial {
			continue
		}
		if m.Type != zerobus.TypeMethodReturn || m.Signature != "a{ta{sv}}" {
			t.Fatalf("GetWindows reply: type %d signature %q", m.Type, m.Signature)
		}
		break
	}
}

func TestRequestNameErrorMessage(t *testing.T) {
	addr, _ := startBus(t)
	server, err := zerobus.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	defer boundIO(t, "invalid name reply", func() { _ = server.Close() })()
	// Compare the same daemon error with zerobus's synchronous decoder.
	_, wantErr := server.RequestName("invalid", zerobus.NameFlagDoNotQueue)
	var want *zerobus.Error
	if !errors.As(wantErr, &want) || want.Message == "" {
		t.Fatalf("expected daemon error with message: %v", wantErr)
	}
	s := &Service{conn: server}
	_, gotErr := s.requestName("invalid")
	var got *zerobus.Error
	if !errors.As(gotErr, &got) {
		t.Fatalf("expected D-Bus error: %v", gotErr)
	}
	if got.Name != want.Name || got.Message != want.Message {
		t.Fatalf("error = %#v, want %#v", got, want)
	}
}

func TestAcquireConnectionClosedAfterUnrelatedReply(t *testing.T) {
	// No sockets or timing: a generated mock supplies the unrelated reply and
	// the closed transport, in order. Neither may be mistaken for our reply.
	b := newMockbus(t)
	s := &Service{conn: b, ctx: context.Background()}
	b.EXPECT().AddMatch(mock.Anything).Return(nil).Once()
	encoder := new(zerobus.Encoder)
	b.EXPECT().NewMethodCall(busDaemon, busDaemonPath, busDaemon, "RequestName", "su").Return(encoder, uint32(42)).Once()
	b.EXPECT().Send().Return(uint32(42), nil).Once()
	first := b.EXPECT().ReadMessage().Return(&zerobus.Message{Type: zerobus.TypeMethodReturn, Sender: busDaemon, ReplySerial: 41}, nil).Once()
	b.EXPECT().ReadMessage().Return(nil, zerobus.ErrClosed).Once().NotBefore(first)
	if err := s.acquire(); !errors.Is(err, zerobus.ErrClosed) {
		t.Fatalf("acquire error: %v", err)
	}
}
