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

// privateBus starts a dbus-daemon of its own, so tests never touch the
// desktop's session bus. CI must run these tests: there, a missing daemon
// fails.
func privateBus(t *testing.T) string {
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
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(line)
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
}

type harnessOpts struct {
	allow  *captureallow.Policy
	noPW   bool
	holdOp bool
}

func newSvcHarness(t *testing.T, o harnessOpts) *svcHarness {
	t.Helper()
	h := &svcHarness{t: t, addr: privateBus(t), cmds: make(chan ports.CastCommand, 64), peers: &peers{}, log: &logBuf{}, done: make(chan struct{})}
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
		return os.Open(os.DevNull)
	}).Maybe()
	res.EXPECT().Exe(mock.Anything).RunAndReturn(func(*os.File) (string, int, error) {
		open := int(h.peers.pid.Load())
		pid := open
		if h.peers.exePid != nil {
			pid = h.peers.exePid(open)
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
		h.core.EXPECT().Iterate().Maybe()
		h.core.EXPECT().Close().Return().Maybe()
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
