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
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerobus"
	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/mock"
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
	line, err := bufio.NewReader(out).ReadString('\n')
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

// peers is what the mocked peerResolver answers: the executable of every
// peer, and a hook to hold Open.
type peers struct {
	exe     atomic.Value // string
	exeNow  func() string
	openGo  chan struct{} // when set, Open waits for it
	pid     atomic.Int64
	openErr error
	exePid  func(open int) int // pid Exe reports; the opened one when nil

	// exeFile, when set, tells the executable of one pidfd: the pidfds are
	// numbered in the order Open handed them out (0 is the first peer).
	exeFile func(n int) string
	mu      sync.Mutex
	opened  []*os.File
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
	cmds  chan ports.CastCommand
	peers *peers
	core  *mockvideoCore
	run   *mockrunner
	log   *logBuf
	done  chan struct{}

	killBus func()        // loses the bus: the daemon dies
	closed  chan struct{} // a PipeWire connection was closed
}

type harnessOpts struct {
	allow  *captureallow.Policy
	noPW   bool
	holdOp bool
	// iterate runs inside every PipeWire loop iteration, on the owner.
	iterate func()
}

func newSvcHarness(t *testing.T, o harnessOpts) *svcHarness {
	t.Helper()
	h := &svcHarness{t: t, cmds: make(chan ports.CastCommand, 64), peers: &peers{}, log: &logBuf{}, done: make(chan struct{}), closed: make(chan struct{}, 8)}
	h.addr, h.killBus = startBus(t)
	h.peers.exe.Store("/usr/bin/grim")
	if o.holdOp {
		h.peers.openGo = make(chan struct{})
	}
	allow := o.allow
	if allow == nil {
		allow = captureallow.Builtin()
	}
	h.svc = New(func() *captureallow.Policy { return allow }, h.cmds, testLog(h.log))
	h.svc.address = h.addr

	res := newMockpeerResolver(t)
	res.EXPECT().Open(mock.Anything).RunAndReturn(func(pid int) (*os.File, error) {
		if h.peers.openGo != nil {
			<-h.peers.openGo
		}
		if h.peers.openErr != nil {
			return nil, h.peers.openErr
		}
		h.peers.pid.Store(int64(pid))
		f, err := os.Open(os.DevNull)
		if err == nil {
			h.peers.mu.Lock()
			h.peers.opened = append(h.peers.opened, f)
			h.peers.mu.Unlock()
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

// waitName waits until the service owns name.
func (h *svcHarness) waitName(c *zerobus.Conn, name string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e := c.NewCall("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "NameHasOwner", "s")
		e.Str(name)
		m, err := c.Call()
		if err != nil {
			h.t.Fatal(err)
		}
		if m.Body().Bool() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("%s not owned", name)
}

// callErr calls and returns the D-Bus error name, "" on success.
func callErr(c *zerobus.Conn, dest, path, iface, member, sig string, body func(*zerobus.Encoder)) (string, *zerobus.Message) {
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
