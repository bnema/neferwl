package powerkey

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerobus"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
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
	checkStartup := boundIO(t, "dbus-daemon startup", func() { _ = cmd.Process.Kill(); _ = out.Close() })
	line, err := bufio.NewReader(out).ReadString('\n')
	checkStartup()
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
	conn     *zerobus.Conn
	commands chan func()
	grants   chan grant
	done     chan struct{}
}

func newPeer(t *testing.T, address string) *logindPeer {
	t.Helper()
	conn, err := zerobus.Dial(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	p := &logindPeer{conn: conn, commands: make(chan func()), grants: make(chan grant, 8)}
	checkName := boundIO(t, "RequestName", func() { _ = conn.Close() })
	if err := p.own(); err != nil {
		t.Fatal(err)
	}
	checkName()
	done := make(chan struct{})
	p.done = done
	go func() {
		defer close(done)
		for {
			select {
			case f := <-p.commands:
				f()
			default:
			}
			if !conn.Buffered() {
				raw, err := conn.SyscallConn()
				if err != nil {
					return
				}
				n := 0
				if err := raw.Control(func(fd uintptr) { n, _ = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 10) }); err != nil {
					return
				}
				if n == 0 {
					continue
				}
			}
			m, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if m.Type != zerobus.TypeMethodCall || m.Member != "Inhibit" {
				continue
			}
			r := m.Body()
			what := r.Str()
			who := r.Str()
			why := r.Str()
			mode := r.Str()
			if r.Err() != nil || who != "neferwl" || why != "power keys are bound in the compositor" {
				t.Error("invalid Inhibit arguments")
				return
			}
			rd, w, err := os.Pipe()
			if err != nil {
				t.Error(err)
				return
			}
			conn.NewReply(m, "h").UnixFD(int(w.Fd()))
			if _, err := conn.Send(); err != nil {
				_ = rd.Close()
				_ = w.Close()
				return
			}
			select {
			case p.grants <- grant{what: what, mode: mode, hold: w, released: rd}:
			case <-time.After(5 * time.Second):
				_ = w.Close()
				_ = rd.Close()
				t.Error("grant delivery timed out")
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("peer did not stop")
			return
		}
		for {
			select {
			case g := <-p.grants:
				_ = g.hold.Close()
				_ = g.released.Close()
			default:
				return
			}
		}
	})
	return p
}

func (p *logindPeer) own() error {
	r, err := p.conn.RequestName(logind, zerobus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if r != zerobus.NamePrimaryOwner {
		return fmt.Errorf("RequestName: got %d", r)
	}
	return nil
}

func (p *logindPeer) next(t *testing.T) grant {
	t.Helper()
	select {
	case g := <-p.grants:
		t.Cleanup(func() { _ = g.hold.Close(); _ = g.released.Close() })
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

// wait blocks until the log has n lines containing msg.
func (l *logBuf) wait(t *testing.T, msg string, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); l.count(msg) < n; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("log never had %d x %q", n, msg)
		}
	}
}

// holding waits until Serve has received n lock answers: only then does it
// own the descriptor.
func (l *logBuf) holding(t *testing.T, n int) {
	t.Helper()
	l.wait(t, "holding logind key locks", n)
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

// When logind restarts, the locks are taken again and the old ones closed.
func TestServeTakesTheLockAgainAfterLogindRestarts(t *testing.T) {
	address := startBus(t)
	peer := newPeer(t, address)
	_, log := run(t, address)
	first := peer.next(t)
	log.holding(t, 1)
	if _, err := peer.release(t); err != nil {
		t.Fatal(err)
	}
	log.wait(t, "ignored until the next attempt", 1)
	// The gap: the old lock is still ours, logind keeps it through a restart.
	peer.reown(t)
	peer.next(t)
	log.holding(t, 2)
	if !released(t, first) {
		t.Fatal("the first lock was not closed once the second was taken")
	}
}

// Without logind on the bus, Serve keeps trying until it appears.
func TestServeWaitsForLogind(t *testing.T) {
	address := startBus(t)
	peer := newPeer(t, address)
	if _, err := peer.release(t); err != nil {
		t.Fatal(err)
	}
	_, log := run(t, address)
	log.wait(t, "ignored until the next attempt", 1)
	peer.reown(t)
	peer.next(t)
	log.holding(t, 1)
}

// command reports errors on the test goroutine, never from the peer loop.
func (p *logindPeer) command(t *testing.T, f func() error) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = p.conn.Close() })
	defer stop()
	result := make(chan error, 1)
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case p.commands <- func() { result <- f() }:
	case <-p.done:
		return fmt.Errorf("peer stopped before command")
	case <-timer.C:
		return fmt.Errorf("command submission timed out")
	}
	select {
	case err := <-result:
		return err
	case <-p.done:
		return fmt.Errorf("peer stopped during command")
	case <-timer.C:
		return fmt.Errorf("command result timed out")
	}
}
func (p *logindPeer) release(t *testing.T) (uint32, error) {
	return 0, p.command(t, func() error {
		p.conn.NewCall(busDaemon, "/org/freedesktop/DBus", busDaemon, "ReleaseName", "s").Str(logind)
		_, err := p.conn.Call()
		return err
	})
}
func (p *logindPeer) reown(t *testing.T) {
	t.Helper()
	if err := p.command(t, p.own); err != nil {
		t.Fatal(err)
	}
}
