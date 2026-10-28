package screencast

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/captureallow"
	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/zerobus"
	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// startBus starts a dbus-daemon of its own, so tests never touch the
// desktop's session bus, and returns its address and a function killing it,
// which is how a test loses the bus. CI must run these tests: there, a
// missing daemon fails.
func startBus(t *testing.T) (string, func()) {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("dbus-daemon not installed")
		}
		t.Skip("dbus-daemon not installed")
	}
	cmd := exec.Command(bin, "--session", "--nofork", "--nopidfile", "--print-address=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	kill := func() { _ = cmd.Process.Kill() }
	t.Cleanup(func() { kill(); _ = cmd.Wait() })
	checkStartup := boundIO(t, "dbus-daemon startup", func() { _ = cmd.Process.Kill(); _ = out.Close() })
	line, err := bufio.NewReader(out).ReadString('\n')
	checkStartup()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(line), kill
}

// logBuf is a locked bytes.Buffer, a real io.Writer.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func testLog(out *logBuf) zerowrap.Logger {
	return zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: out}).WithField("component", "screencast")
}

// peers is what the mocked peerResolver answers: the executable and pid
// of every peer.
type peers struct {
	exe     atomic.Value // string
	exeNow  func() string
	pid     atomic.Int64
	openErr error
	exePid  func(open int) int // pid Exe reports; the opened one when nil

	// exeFile, when set, tells the executable of one pidfd: the pidfds are
	// numbered in the order Open handed them out (0 is the first peer).
	exeFile func(n int) string
	mu      sync.Mutex
	opened  []*os.File
	// ends[i] is the write end of the pipe whose read end is opened[i]: it
	// polls POLLERR once the service closed that pidfd.
	ends []*os.File
}

// waitClosed waits until the service closed pidfd i (see ends), within one
// deadline.
func (p *peers) waitClosed(t *testing.T, i int) {
	t.Helper()
	p.mu.Lock()
	end := p.ends[i]
	p.mu.Unlock()
	// No requested events: an empty pipe is always writable, so POLLOUT would
	// return at once. POLLERR (read end closed) is reported regardless.
	fds := []unix.PollFd{{Fd: int32(end.Fd())}}
	for {
		n, err := unix.Poll(fds, 5000)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Fatalf("pidfd %d not closed", i)
		}
		if fds[0].Revents&unix.POLLERR != 0 {
			return
		}
	}
}

func (p *peers) number(f *os.File) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, o := range p.opened {
		if o == f {
			return i
		}
	}
	return -1
}

// svcHarness runs a Service on a private bus with mocked executables and
// PipeWire.
type svcHarness struct {
	t     *testing.T
	addr  string
	svc   *Service
	cmds  chan domain.CastCommand
	peers *peers
	core  *mockvideoCore
	run   *mockrunner
	log   *logBuf
	done  chan struct{}

	killBus func()             // loses the bus: the daemon dies
	cancel  context.CancelFunc // stops the service, as the process exit does
	closed  chan struct{}      // a PipeWire connection was closed
	applied chan struct{}      // the owner applied an inventory
	reaped  chan struct{}      // the owner freed dying sessions
}

type harnessOpts struct {
	allow *captureallow.Policy
	noPW  bool
	// drain overrides how long the stopping service waits for frames.
	drain time.Duration
	// iterate runs inside every PipeWire loop iteration, on the owner.
	iterate func()
}

//testpolicy:realtime service integration uses a private D-Bus daemon and Unix descriptors
func newSvcHarness(t *testing.T, o harnessOpts) *svcHarness {
	t.Helper()
	h := &svcHarness{t: t, cmds: make(chan domain.CastCommand, 64), peers: &peers{}, log: &logBuf{}, done: make(chan struct{}), closed: make(chan struct{}, 8)}
	h.addr, h.killBus = startBus(t)
	h.peers.exe.Store("/usr/bin/grim")
	allow := o.allow
	if allow == nil {
		allow = captureallow.Builtin()
	}
	h.svc = New(func() *captureallow.Policy { return allow }, h.cmds, testLog(h.log))
	h.svc.address = h.addr
	h.applied = make(chan struct{}, 8)
	h.reaped = make(chan struct{}, 8)
	h.svc.inventoryApplied, h.svc.sessionsReaped = h.applied, h.reaped
	if o.drain > 0 {
		h.svc.drain = o.drain
	}

	res := newMockpeerResolver(t)
	res.EXPECT().Open(mock.Anything).RunAndReturn(func(pid int) (*os.File, error) {
		if h.peers.openErr != nil {
			return nil, h.peers.openErr
		}
		h.peers.pid.Store(int64(pid))
		// A pipe's read end stands in for the pidfd: its closing shows on
		// the write end.
		f, w, err := os.Pipe()
		if err == nil {
			h.peers.mu.Lock()
			h.peers.opened = append(h.peers.opened, f)
			h.peers.ends = append(h.peers.ends, w)
			h.peers.mu.Unlock()
			t.Cleanup(func() { _ = w.Close() })
		}
		return f, err
	}).Maybe()
	res.EXPECT().Exe(mock.Anything).RunAndReturn(func(f *os.File) (string, int, error) {
		open := int(h.peers.pid.Load())
		pid := open
		if h.peers.exePid != nil {
			pid = h.peers.exePid(open)
		}
		if h.peers.exeFile != nil {
			return h.peers.exeFile(h.peers.number(f)), pid, nil
		}
		if h.peers.exeNow != nil {
			return h.peers.exeNow(), pid, nil
		}
		return h.peers.exe.Load().(string), pid, nil
	}).Maybe()
	h.svc.peers = res
	h.run = newMockrunner(t)
	h.svc.runner = h.run

	pwOpen := newMockpipewireOpener(t)
	if o.noPW {
		pwOpen.EXPECT().Open().Return(nil, os.ErrNotExist)
	} else {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Close(); w.Close() })
		h.core = newMockvideoCore(t)
		h.core.EXPECT().Fd().Return(int(r.Fd()))
		h.core.EXPECT().Iterate().Run(func() {
			if o.iterate != nil {
				o.iterate()
			}
		}).Maybe()
		h.core.EXPECT().Close().Run(func() {
			select {
			case h.closed <- struct{}{}:
			default:
			}
		}).Maybe()
		pwOpen.EXPECT().Open().Return(h.core, nil)
	}
	h.svc.pw = pwOpen

	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { defer close(h.done); h.svc.Run(ctx, 10*time.Millisecond) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not stop")
		}
	})
	return h
}

// client is a bus connection of the test.
func (h *svcHarness) client() *zerobus.Conn {
	h.t.Helper()
	c, err := zerobus.Dial(h.addr)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = c.Close() })
	return c
}

// waitName waits until the service owns name: it subscribes to the
// NameOwnerChanged of name, then asks once, so an acquisition before the
// subscription is seen by the question and a later one by the signal.
func (h *svcHarness) waitName(c *zerobus.Conn, name string) {
	h.t.Helper()
	check := boundIO(h.t, "waiting for "+name, func() { _ = c.Close() })
	defer check()
	if err := c.AddMatch("type='signal',sender='org.freedesktop.DBus',interface='org.freedesktop.DBus',member='NameOwnerChanged',arg0='" + name + "'"); err != nil {
		h.t.Fatal(err)
	}
	e := c.NewCall("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "NameHasOwner", "s")
	e.Str(name)
	m, err := c.Call()
	if err != nil {
		h.t.Fatal(err)
	}
	if m.Body().Bool() {
		return
	}
	for {
		m, err := c.ReadMessage()
		if err != nil {
			h.t.Fatalf("%s not owned: %v", name, err)
		}
		if m.Type != zerobus.TypeSignal || m.Member != "NameOwnerChanged" {
			continue
		}
		r := m.Body()
		_, _, owner := r.Str(), r.Str(), r.Str()
		if r.Err() == nil && owner != "" {
			return
		}
	}
}

// waitServing waits until all name requests have completed, for tests that
// inspect which names are absent. Calls on early names need only waitName.
func (h *svcHarness) waitServing(c *zerobus.Conn) {
	h.t.Helper()
	h.waitName(c, nameScreenshot)
}

// callErr calls and returns the D-Bus error name, "" on success.
func callErr(t *testing.T, c *zerobus.Conn, dest, path, iface, member, sig string, body func(*zerobus.Encoder)) (string, *zerobus.Message) {
	check := boundIO(t, iface+"."+member, func() { _ = c.Close() })
	defer check()
	e := c.NewCall(dest, path, iface, member, sig)
	if body != nil {
		body(e)
	}
	m, err := c.Call()
	if err == nil {
		return "", m
	}
	if de, ok := err.(*zerobus.Error); ok {
		return de.Name, m
	}
	return err.Error(), m
}
