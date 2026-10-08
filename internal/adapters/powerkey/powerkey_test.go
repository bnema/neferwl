package powerkey

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/godbus/dbus/v5"
)

// startBus starts a dbus-daemon of its own, so tests never touch the
// system bus. CI must run these tests: there, a missing daemon fails.
func startBus(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("dbus-daemon not installed")
		}
		t.Skip("dbus-daemon not installed")
	}
	socket := filepath.Join(t.TempDir(), "bus")
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
	return strings.TrimSpace(line)
}

// grant is one Inhibit call logind answered.
type grant struct {
	what, mode string
	hold       *os.File // our end of the lock: the client has its own copy
	released   *os.File // reads EOF once every copy of the lock is closed
}

// logindPeer plays logind on the private bus: it owns the name and answers
// Inhibit with a pipe, whose other end shows when the client lets go.
type logindPeer struct {
	conn   *dbus.Conn
	grants chan grant
}

func newPeer(t *testing.T, address string) *logindPeer {
	t.Helper()
	conn, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	p := &logindPeer{conn: conn, grants: make(chan grant, 8)}
	table := map[string]any{"Inhibit": func(what, who, why, mode string) (dbus.UnixFD, *dbus.Error) {
		r, w, err := os.Pipe()
		if err != nil {
			return 0, dbus.MakeFailedError(err)
		}
		t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
		fd := dbus.UnixFD(w.Fd()) // before the send: the test closes w after it
		p.grants <- grant{what: what, mode: mode, hold: w, released: r}
		return fd, nil
	}}
	if err := conn.ExportMethodTable(table, logindPath, manager); err != nil {
		t.Fatal(err)
	}
	p.own(t)
	return p
}

func (p *logindPeer) own(t *testing.T) {
	t.Helper()
	if r, err := p.conn.RequestName(logind, dbus.NameFlagDoNotQueue); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatal(r, err)
	}
}

func (p *logindPeer) next(t *testing.T) grant {
	t.Helper()
	select {
	case g := <-p.grants:
		return g
	case <-time.After(5 * time.Second):
		t.Fatal("no Inhibit call")
		return grant{}
	}
}

// logBuf is a log sink the test reads while Serve writes.
type logBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuf) count(s string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Count(l.buf.String(), s)
}

// holding waits until Serve has received n lock answers: only then does it
// own the descriptor.
func (l *logBuf) holding(t *testing.T, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); l.count("holding logind key locks") < n; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("Serve never held lock %d", n)
		}
	}
}

func run(t *testing.T, address string) (stop func(), log *logBuf) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	log = &logBuf{}
	logger := zerowrap.New(zerowrap.Config{Level: "info", Format: "json", Output: log}).WithField("component", "powerkey")
	go func() {
		defer close(done)
		Serve(ctx, address, 10*time.Millisecond, logger)
	}()
	var once bool
	stop = func() {
		if once {
			return
		}
		once = true
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Serve outlived its context")
		}
	}
	t.Cleanup(stop)
	return stop, log
}

// released reports whether every copy of the lock is closed, once the
// peer's own copy is.
func released(t *testing.T, g grant) bool {
	t.Helper()
	_ = g.hold.Close()
	_ = g.released.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err := io.ReadAll(g.released)
	return err == nil
}

// Serve blocks the four key handlers and lets go of them when it stops.
func TestServeHoldsAndReleasesTheLock(t *testing.T) {
	address := startBus(t)
	peer := newPeer(t, address)
	stop, log := run(t, address)
	g := peer.next(t)
	log.holding(t, 1)
	if g.what != what || g.mode != "block" {
		t.Fatalf("Inhibit(%q, %q)", g.what, g.mode)
	}
	stop()
	if !released(t, g) {
		t.Fatal("the lock outlived Serve")
	}
}

// When logind restarts, the locks are taken again.
func TestServeTakesTheLockAgainAfterLogindRestarts(t *testing.T) {
	address := startBus(t)
	peer := newPeer(t, address)
	_, log := run(t, address)
	peer.next(t)
	log.holding(t, 1)
	if _, err := peer.conn.ReleaseName(logind); err != nil {
		t.Fatal(err)
	}
	peer.own(t)
	peer.next(t)
	log.holding(t, 2)
}

// Without logind on the bus, Serve keeps trying until it appears.
func TestServeWaitsForLogind(t *testing.T) {
	address := startBus(t)
	peer := newPeer(t, address)
	if _, err := peer.conn.ReleaseName(logind); err != nil {
		t.Fatal(err)
	}
	_, log := run(t, address)
	time.Sleep(50 * time.Millisecond)
	peer.own(t)
	peer.next(t)
	log.holding(t, 1)
}

func TestLostTrustsOnlyTheBus(t *testing.T) {
	body := []any{logind, "", ""}
	for name, tc := range map[string]struct {
		sig  *dbus.Signal
		want bool
	}{
		"bus, logind gone":  {&dbus.Signal{Sender: busDaemon, Name: busDaemon + ".NameOwnerChanged", Body: body}, true},
		"bus, logind moved": {&dbus.Signal{Sender: busDaemon, Name: busDaemon + ".NameOwnerChanged", Body: []any{logind, "", ":1.5"}}, false},
		"bus, other name":   {&dbus.Signal{Sender: busDaemon, Name: busDaemon + ".NameOwnerChanged", Body: []any{"x.y", "", ""}}, false},
		"client forging it": {&dbus.Signal{Sender: ":1.9", Name: busDaemon + ".NameOwnerChanged", Body: body}, false},
		"other signal":      {&dbus.Signal{Sender: busDaemon, Name: busDaemon + ".NameAcquired", Body: body}, false},
		"malformed body":    {&dbus.Signal{Sender: busDaemon, Name: busDaemon + ".NameOwnerChanged", Body: []any{1, 2, 3}}, false},
		"short body":        {&dbus.Signal{Sender: busDaemon, Name: busDaemon + ".NameOwnerChanged", Body: []any{logind}}, false},
	} {
		if got := lost(tc.sig); got != tc.want {
			t.Errorf("%s: lost = %v, want %v", name, got, tc.want)
		}
	}
}
