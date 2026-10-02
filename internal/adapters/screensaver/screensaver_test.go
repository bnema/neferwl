package screensaver

import (
	"bufio"
	"context"
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
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not installed")
	}
	cmd := exec.Command(bin, "--session", "--nofork", "--nopidfile", "--print-address=1", "--address=unix:path="+filepath.Join(t.TempDir(), "bus"))
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

// serve runs the service on address and returns its held reports.
func serve(t *testing.T, address string) (<-chan bool, chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := New(ctx, address, logging.For(ctx, "screensaver"))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	held := make(chan bool, 8)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, held) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	// Wait for the name: Run requests it after exporting.
	probe := connect(t, address)
	deadline := time.Now().Add(2 * time.Second)
	for {
		var has bool
		if err := probe.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, busName).Store(&has); err != nil {
			t.Fatal(err)
		}
		if has {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("name never owned")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return held, done
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
	held, _ := serve(t, address)
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
	var active bool
	if err := a.Object(busName, "/org/freedesktop/ScreenSaver").Call(iface+".GetActive", 0).Store(&active); err != nil || active {
		t.Fatalf("GetActive %v %v", active, err)
	}
	var xml string
	if err := a.Object(busName, "/org/freedesktop/ScreenSaver").Call("org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&xml); err != nil || !strings.Contains(xml, `<method name="UnInhibit">`) {
		t.Fatalf("introspection %q %v", xml, err)
	}
}

// A client that disconnects without UnInhibit, as a crash does, releases
// its inhibitions.
func TestDisconnectReleases(t *testing.T) {
	address := privateBus(t)
	held, _ := serve(t, address)
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
	go func() { done <- s.Run(ctx, held) }()
	c := connect(t, address)
	deadline := time.Now().Add(2 * time.Second)
	for {
		var cookie uint32
		if c.Object(busName, "/org/freedesktop/ScreenSaver").Call(iface+".Inhibit", 0, "test", "video").Store(&cookie) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never served")
		}
		time.Sleep(5 * time.Millisecond)
	}
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

// Another owner keeps the name: the service stands aside without error.
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
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, make(chan bool)) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
