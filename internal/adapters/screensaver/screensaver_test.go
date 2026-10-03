package screensaver

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/logging"
	"github.com/godbus/dbus/v5"
)

// privateBus starts a dbus-daemon of its own, so tests never touch the
// desktop's session bus.
func privateBus(t *testing.T) string {
	t.Helper()
	address, _ := startBus(t)
	return address
}

// startBus also returns the daemon, for tests that kill it.
func startBus(t *testing.T) (string, *exec.Cmd) {
	t.Helper()
	return startBusAt(t, filepath.Join(t.TempDir(), "bus"))
}

// startBusAt listens on socket, so a test can restart a bus at the same
// address. CI must run these tests: there, a missing daemon fails.
func startBusAt(t *testing.T, socket string) (string, *exec.Cmd) {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("dbus-daemon not installed")
		}
		t.Skip("dbus-daemon not installed")
	}
	_ = os.Remove(socket)
	cmd := exec.Command(bin, "--session", "--nofork", "--nopidfile", "--print-address=1", "--address=unix:path="+socket)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(line), cmd
}

func connect(t *testing.T, address string) *dbus.Conn {
	t.Helper()
	c, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// served is a running service and what it reports.
type served struct {
	s        *Service
	held     chan bool
	activity chan struct{}
}

// serve runs the service on address until the test ends.
func serve(t *testing.T, address string) served {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := New(ctx, address, logging.For(ctx, "screensaver"))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	v := served{s: s, held: make(chan bool, 8), activity: make(chan struct{}, 1)}
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, Reports{Held: v.held, Activity: v.activity}) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	// Wait for the name: Run requests it after exporting.
	waitOwner(t, connect(t, address), func(owner string) bool { return owner != "" })
	return v
}

// waitOwner polls the owner of busName ("" when none) until ok accepts it.
func waitOwner(t *testing.T, probe *dbus.Conn, ok func(string) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var owner string
		_ = probe.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, busName).Store(&owner)
		if ok(owner) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("owner %q not accepted", owner)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func next(t *testing.T, held <-chan bool, want bool) {
	t.Helper()
	select {
	case got := <-held:
		if got != want {
			t.Fatalf("held %v, want %v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no held %v", want)
	}
}

func none(t *testing.T, held <-chan bool) {
	t.Helper()
	select {
	case got := <-held:
		t.Fatalf("unexpected held %v", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func inhibit(t *testing.T, c *dbus.Conn, path dbus.ObjectPath) uint32 {
	t.Helper()
	var cookie uint32
	if err := c.Object(busName, path).Call(iface+".Inhibit", 0, "test", "video").Store(&cookie); err != nil {
		t.Fatal(err)
	}
	return cookie
}

func TestInhibitAndUnInhibit(t *testing.T) {
	address := privateBus(t)
	held := serve(t, address).held
	a, b := connect(t, address), connect(t, address)
	ca := inhibit(t, a, "/org/freedesktop/ScreenSaver")
	next(t, held, true)
	cb := inhibit(t, b, "/ScreenSaver")
	none(t, held)
	// b cannot release a's cookie.
	if err := b.Object(busName, "/org/freedesktop/ScreenSaver").Call(iface+".UnInhibit", 0, ca).Err; err != nil {
		t.Fatal(err)
	}
	if err := a.Object(busName, "/org/freedesktop/ScreenSaver").Call(iface+".UnInhibit", 0, ca).Err; err != nil {
		t.Fatal(err)
	}
	none(t, held)
	if err := b.Object(busName, "/ScreenSaver").Call(iface+".UnInhibit", 0, cb).Err; err != nil {
		t.Fatal(err)
	}
	next(t, held, false)
	obj := a.Object(busName, "/org/freedesktop/ScreenSaver")
	var active bool
	if err := obj.Call(iface+".GetActive", 0).Store(&active); err != nil || active {
		t.Fatalf("GetActive %v %v", active, err)
	}
	var since uint32
	if err := obj.Call(iface+".GetActiveTime", 0).Store(&since); err != nil || since != 0 {
		t.Fatalf("GetActiveTime %v %v", since, err)
	}
	if err := obj.Call(iface+".SetActive", 0, true).Store(&active); err != nil || active {
		t.Fatalf("SetActive %v %v", active, err)
	}
	var xml string
	if err := obj.Call("org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&xml); err != nil || !strings.Contains(xml, `<method name="UnInhibit">`) || !strings.Contains(xml, `<method name="SimulateUserActivity">`) {
		t.Fatalf("introspection %q %v", xml, err)
	}
}

// SimulateUserActivity reports activity without blocking: calls the
// reader has not taken yet collapse into one.
func TestSimulateUserActivity(t *testing.T) {
	address := privateBus(t)
	v := serve(t, address)
	obj := connect(t, address).Object(busName, "/ScreenSaver")
	for range 3 {
		if err := obj.Call(iface+".SimulateUserActivity", 0).Err; err != nil {
			t.Fatal(err)
		}
	}
	<-v.activity
	select {
	case <-v.activity:
		t.Fatal("activity not coalesced")
	default:
	}
}

// A client that disconnects without UnInhibit, as a crash does, releases
// its inhibitions.
func TestDisconnectReleases(t *testing.T) {
	address := privateBus(t)
	held := serve(t, address).held
	c, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	inhibit(t, c, "/org/freedesktop/ScreenSaver")
	inhibit(t, c, "/org/freedesktop/ScreenSaver")
	next(t, held, true)
	_ = c.Close()
	next(t, held, false)
}

// A NameOwnerChanged sent straight to the service by a client is not the
// bus's: it cannot end another client's inhibition.
func TestSpoofedDepartureIgnored(t *testing.T) {
	address := privateBus(t)
	held := serve(t, address).held
	victim, attacker := connect(t, address), connect(t, address)
	inhibit(t, victim, "/org/freedesktop/ScreenSaver")
	next(t, held, true)
	var service string
	if err := attacker.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, busName).Store(&service); err != nil {
		t.Fatal(err)
	}
	gone := victim.Names()[0]
	msg := &dbus.Message{
		Type: dbus.TypeSignal,
		Headers: map[dbus.HeaderField]dbus.Variant{
			dbus.FieldPath:        dbus.MakeVariant(dbus.ObjectPath("/org/freedesktop/DBus")),
			dbus.FieldInterface:   dbus.MakeVariant("org.freedesktop.DBus"),
			dbus.FieldMember:      dbus.MakeVariant("NameOwnerChanged"),
			dbus.FieldDestination: dbus.MakeVariant(service),
			dbus.FieldSignature:   dbus.MakeVariant(dbus.SignatureOf("", "", "")),
		},
		Body: []any{gone, gone, ""},
	}
	if call := attacker.Send(msg, nil); call != nil && call.Err != nil {
		t.Fatal(call.Err)
	}
	// A call after the signal: the service has read the signal by its reply.
	inhibit(t, attacker, "/ScreenSaver")
	none(t, held)
	_ = attacker.Close()
	none(t, held)
}

// A client that leaves right after its Inhibit, before the service handles
// the call, holds nothing: its departure may come first.
func TestEarlyDepartureHoldsNothing(t *testing.T) {
	address := privateBus(t)
	held := serve(t, address).held
	for range 20 {
		c, err := dbus.Connect(address)
		if err != nil {
			t.Fatal(err)
		}
		c.Object(busName, "/ScreenSaver").Go(iface+".Inhibit", dbus.FlagNoReplyExpected, nil, "test", "video")
		_ = c.Close()
	}
	// Each one may hold briefly; once all are handled, none does.
	state := false
	deadline := time.After(3 * time.Second)
	for quiet := false; !quiet; {
		select {
		case state = <-held:
		case <-time.After(300 * time.Millisecond):
			quiet = true
		case <-deadline:
			t.Fatal("never settled")
		}
	}
	if state {
		t.Fatal("a departed client still holds an inhibition")
	}
}

// One connection holds at most maxPerSender cookies; the next is refused.
func TestPerSenderLimit(t *testing.T) {
	address := privateBus(t)
	held := serve(t, address).held
	c := connect(t, address)
	obj := c.Object(busName, "/ScreenSaver")
	for range maxPerSender {
		inhibit(t, c, "/ScreenSaver")
	}
	next(t, held, true)
	var cookie uint32
	if err := obj.Call(iface+".Inhibit", 0, "test", "video").Store(&cookie); err == nil {
		t.Fatalf("cookie %d past the limit", cookie)
	}
	// Another connection is not limited by the first.
	inhibit(t, connect(t, address), "/ScreenSaver")
}

// A call that reaches the handler after Run returned fails at once
// instead of waiting for a loop that is gone.
func TestCallAfterStop(t *testing.T) {
	address := privateBus(t)
	ctx, cancel := context.WithCancel(context.Background())
	s, err := New(ctx, address, logging.For(ctx, "screensaver"))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, Reports{Held: make(chan bool, 1), Activity: make(chan struct{}, 1)}) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := make(chan *dbus.Error, 1)
	go func() { _, err := (handler{s: s}).Inhibit(":1.9", "test", "video"); got <- err }()
	select {
	case err := <-got:
		if err == nil {
			t.Fatal("Inhibit succeeded on a stopped service")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Inhibit waits on a stopped service")
	}
}

// The bus going away while inhibited reports false so idle resumes.
func TestBusLossReleases(t *testing.T) {
	address, cmd := startBus(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := New(ctx, address, logging.For(ctx, "screensaver"))
	if err != nil {
		t.Fatal(err)
	}
	held := make(chan bool, 8)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, Reports{Held: held, Activity: make(chan struct{}, 1)}) }()
	c := connect(t, address)
	waitOwner(t, c, func(owner string) bool { return owner != "" })
	inhibit(t, c, "/ScreenSaver")
	next(t, held, true)
	_ = cmd.Process.Kill()
	next(t, held, false)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("bus loss is not an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
}

// Another owner keeps the name: Run stands aside at once, so it holds no
// bus connection.
func TestNameTaken(t *testing.T) {
	address := privateBus(t)
	other := connect(t, address)
	if r, err := other.RequestName(busName, dbus.NameFlagDoNotQueue); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatal(r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s, err := New(ctx, address, logging.For(ctx, "screensaver"))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, Reports{Held: make(chan bool), Activity: make(chan struct{}, 1)}) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNameTaken) {
			t.Fatalf("err %v, want ErrNameTaken", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run waits instead of standing aside")
	}
	var owner string
	if err := other.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, busName).Store(&owner); err != nil || owner != other.Names()[0] {
		t.Fatalf("owner %q, want %q: %v", owner, other.Names()[0], err)
	}
}

// Serve takes the name once its owner lets it go, and serves again after
// the bus restarts.
func TestServeRetries(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "bus")
	address, cmd := startBusAt(t, socket)
	other := connect(t, address)
	if r, err := other.RequestName(busName, dbus.NameFlagDoNotQueue); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatal(r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	held := make(chan bool, 8)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		Serve(ctx, address, Reports{Held: held, Activity: make(chan struct{}, 1)}, 10*time.Millisecond, logging.For(ctx, "screensaver"))
	}()
	t.Cleanup(func() { cancel(); <-stopped })
	if _, err := other.ReleaseName(busName); err != nil {
		t.Fatal(err)
	}
	mine := other.Names()[0]
	waitOwner(t, other, func(owner string) bool { return owner != "" && owner != mine })
	inhibit(t, other, "/ScreenSaver")
	next(t, held, true)

	// The bus restarts: the inhibition is gone with it, the name comes back.
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	next(t, held, false)
	address, _ = startBusAt(t, socket)
	waitOwner(t, connect(t, address), func(owner string) bool { return owner != "" })
}
