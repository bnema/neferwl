package wayland

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/securitycontext"
	"github.com/bnema/neferwl/internal/adapters/logging"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

// sandboxListener is what a sandbox engine prepares: a listening socket and
// the pipe whose hangup ends the context.
type sandboxListener struct {
	path     string
	listen   int // engine's copy; the compositor gets a dup
	closeR   int // goes to the compositor
	closeW   int // the engine keeps it; closing it hangs up closeR
	released bool
}

func newSandboxListener(t *testing.T) *sandboxListener {
	t.Helper()
	// Unix socket paths are short; t.TempDir can be long.
	dir, err := os.MkdirTemp("", "sbx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	l := &sandboxListener{path: filepath.Join(dir, "s")}
	if l.listen, err = unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0); err != nil {
		t.Fatal(err)
	}
	if err = unix.Bind(l.listen, &unix.SockaddrUnix{Name: l.path}); err != nil {
		t.Fatal(err)
	}
	if err = unix.Listen(l.listen, 8); err != nil {
		t.Fatal(err)
	}
	var p [2]int
	if err = unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	l.closeR, l.closeW = p[0], p[1]
	t.Cleanup(func() {
		if !l.released {
			_ = unix.Close(l.listen)
		}
		_ = unix.Close(l.closeR)
		if l.closeW >= 0 {
			_ = unix.Close(l.closeW)
		}
	})
	return l
}

// handOver drops the engine's descriptors the compositor has its own copies
// of (the protocol: closing them is the only valid operation afterwards). The
// write end of the pipe stays with the engine.
func (l *sandboxListener) handOver() {
	_ = unix.Close(l.listen)
	_ = unix.Close(l.closeR)
	l.released, l.closeR = true, -1
}

func (l *sandboxListener) hangUp() {
	_ = unix.Close(l.closeW)
	l.closeW = -1
}

func (l *sandboxListener) connect(t *testing.T) *wlturbo.Display {
	t.Helper()
	c, err := wlturbo.Connect(l.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(); forgetWireClient(c) })
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return c
}

// refused reports whether nothing listens on the socket any more.
func (l *sandboxListener) refused() bool {
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	return errors.Is(unix.Connect(fd, &unix.SockaddrUnix{Name: l.path}), unix.ECONNREFUSED)
}

func waitTrue(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timeout: " + what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// contextClient is a client of the main socket with a security context being built.
type contextClient struct {
	c       *wlturbo.Display
	manager uint32
	ctx     uint32
}

func newContextClient(t *testing.T, h *admissionHarness) *contextClient {
	t.Helper()
	c := protocolClient(t, h.s, h.dir)
	return &contextClient{c: c, manager: bindProtocol(t, c, "wp_security_context_manager_v1")}
}

// create sends create_listener with the engine's descriptors.
func (cc *contextClient) create(t *testing.T, listen, closeFD int) {
	t.Helper()
	cc.ctx = cc.c.AllocateID()
	if err := wireRequest(cc.c, cc.manager, uint16(securitycontext.WpSecurityContextManagerV1RequestCreateListener), []int{listen, closeFD}, cc.ctx); err != nil {
		t.Fatal(err)
	}
}

func (cc *contextClient) request(t *testing.T, op uint32, args ...any) {
	t.Helper()
	requestProtocol(t, cc.c, cc.ctx, op, args...)
}

// commit makes a context with an engine, like a Flatpak would, and waits for
// the compositor to have processed it.
func (cc *contextClient) commit(t *testing.T, l *sandboxListener, engine, app string) {
	t.Helper()
	cc.create(t, l.listen, l.closeR)
	cc.request(t, securitycontext.WpSecurityContextV1RequestSetSandboxEngine, engine)
	if app != "" {
		cc.request(t, securitycontext.WpSecurityContextV1RequestSetAppId, app)
	}
	cc.request(t, securitycontext.WpSecurityContextV1RequestCommit)
	if err := cc.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	l.handOver()
}

func (h *admissionHarness) sandboxedCount() int {
	n := -1
	h.s.display.Do(func() { n = len(h.s.sandboxed) })
	return n
}

// A client that connects through a committed context is sandboxed: it cannot
// capture, while a client of the main socket still can (the allowlist is `*` here).
func TestSecurityContextSandboxedClientCannotCapture(t *testing.T) {
	h := newAdmissionHarnessWith(t, maxCaptureInflight+2, Options{CaptureAllow: allowStore(t, "*\n")}, logging.For(context.Background(), "wayland"))
	plain := h.client(t)
	if _, ok := h.accepted(t, plain.captureWlr(t)); !ok {
		t.Fatal("plain client refused")
	}

	l := newSandboxListener(t)
	cc := newContextClient(t, h)
	cc.commit(t, l, "org.flatpak", "org.example.App")

	sandboxed := h.clientOn(t, l.connect(t))
	waitTrue(t, "client tagged", func() bool { return h.sandboxedCount() == 1 })
	for i := 0; i < 2; i++ {
		if _, ok := h.accepted(t, sandboxed.captureWlr(t)); ok {
			t.Fatal("sandboxed wlr capture allowed")
		}
	}
	if !sandboxed.extSessionStopped(t) {
		t.Fatal("sandboxed ext capture allowed")
	}
	// The committing client is not sandboxed by it, and is unaffected.
	if _, ok := h.accepted(t, plain.captureWlr(t)); !ok {
		t.Fatal("plain client refused after the context")
	}
	// The tag goes with the client.
	_ = sandboxed.c.Close()
	waitTrue(t, "tag dropped", func() bool { return h.sandboxedCount() == 0 })
}

// Sandboxed clients refuse even when the allowlist is off or lists them.
func TestSecurityContextOverridesAllowlist(t *testing.T) {
	self, _ := os.Executable()
	for _, content := range []string{"*\n", self + "\n"} {
		h, out := authHarness(t, allowStore(t, content), nil)
		l := newSandboxListener(t)
		newContextClient(t, h).commit(t, l, "org.flatpak", "")
		c := h.clientOn(t, l.connect(t))
		if _, ok := h.accepted(t, c.captureWlr(t)); ok {
			t.Fatalf("sandboxed client captured with allowlist %q", content)
		}
		if !strings.Contains(out.String(), `"reason":"sandboxed client"`) {
			t.Fatalf("log = %s", out.String())
		}
	}
}

// The listener keeps serving after the client that created it is gone, takes
// several connections, and stops when close_fd hangs up.
func TestSecurityContextListenerLifecycle(t *testing.T) {
	h := newAdmissionHarness(t, maxCaptureInflight+2)
	l := newSandboxListener(t)
	cc := newContextClient(t, h)
	cc.commit(t, l, "org.flatpak", "org.example.App")
	_ = cc.c.Close() // the protocol: the listener outlives the client that made it
	waitTrue(t, "creator gone", func() bool {
		n := 0
		h.s.display.Do(func() { n = len(h.s.peers) })
		return n == 0
	})
	a, b := h.clientOn(t, l.connect(t)), h.clientOn(t, l.connect(t))
	waitTrue(t, "two tagged", func() bool { return h.sandboxedCount() == 2 })
	for _, c := range []*admissionClient{a, b} {
		if _, ok := h.accepted(t, c.captureWlr(t)); ok {
			t.Fatal("sandboxed client captured")
		}
	}
	if h.s.securityLive.Load() != 1 {
		t.Fatalf("listeners = %d", h.s.securityLive.Load())
	}
	l.hangUp()
	waitTrue(t, "listener stopped", func() bool { return h.s.securityLive.Load() == 0 && l.refused() })
	// Clients already connected are untouched.
	if err := a.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
}

// Compositor shutdown ends the listeners and closes their sockets.
func TestSecurityContextStopsWithTheCompositor(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Options{RuntimeDir: dir}, Channels{}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer cancel()
	c := protocolClient(t, s, dir)
	manager := bindProtocol(t, c, "wp_security_context_manager_v1")
	l := newSandboxListener(t)
	id := c.AllocateID()
	if err := wireRequest(c, manager, uint16(securitycontext.WpSecurityContextManagerV1RequestCreateListener), []int{l.listen, l.closeR}, id); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, id, securitycontext.WpSecurityContextV1RequestSetSandboxEngine, "org.flatpak")
	requestProtocol(t, c, id, securitycontext.WpSecurityContextV1RequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	l.handOver()
	waitTrue(t, "listening", func() bool { return s.securityLive.Load() == 1 })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if s.securityLive.Load() != 0 || !l.refused() {
		t.Fatalf("listener left behind: live=%d refused=%v", s.securityLive.Load(), l.refused())
	}
}

// Destroying a context that was never committed closes the descriptors: the
// socket stops listening.
func TestSecurityContextDestroyBeforeCommit(t *testing.T) {
	h := newAdmissionHarness(t, 2)
	l := newSandboxListener(t)
	cc := newContextClient(t, h)
	cc.create(t, l.listen, l.closeR)
	if err := cc.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	l.handOver()
	if l.refused() {
		t.Fatal("socket stopped listening before the context was destroyed")
	}
	cc.request(t, securitycontext.WpSecurityContextV1RequestDestroy)
	if err := cc.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	waitTrue(t, "descriptors closed", l.refused)
	if h.s.securityLive.Load() != 0 {
		t.Fatal("an uncommitted context listens")
	}
}

func TestSecurityContextErrors(t *testing.T) {
	const (
		ctxAlreadyUsed = uint32(securitycontext.WpSecurityContextV1ErrorAlreadyUsed)
		ctxAlreadySet  = uint32(securitycontext.WpSecurityContextV1ErrorAlreadySet)
		ctxInvalidMeta = uint32(securitycontext.WpSecurityContextV1ErrorInvalidMetadata)
	)
	engine := uint32(securitycontext.WpSecurityContextV1RequestSetSandboxEngine)
	app := uint32(securitycontext.WpSecurityContextV1RequestSetAppId)
	inst := uint32(securitycontext.WpSecurityContextV1RequestSetInstanceId)
	commit := uint32(securitycontext.WpSecurityContextV1RequestCommit)
	type step struct {
		op  uint32
		arg []any
	}
	tests := []struct {
		name  string
		steps []step
		code  uint32
	}{
		{"engine twice", []step{{engine, []any{"a"}}, {engine, []any{"b"}}}, ctxAlreadySet},
		{"app id twice", []step{{app, []any{"a"}}, {app, []any{"b"}}}, ctxAlreadySet},
		{"instance id twice", []step{{inst, []any{"a"}}, {inst, []any{"b"}}}, ctxAlreadySet},
		{"empty engine", []step{{engine, []any{""}}}, ctxInvalidMeta},
		{"control characters", []step{{app, []any{"a\x01b"}}}, ctxInvalidMeta},
		{"oversized", []step{{app, []any{strings.Repeat("x", maxSecurityMetadata+1)}}}, ctxInvalidMeta},
		{"commit without engine", []step{{commit, nil}}, ctxInvalidMeta},
		{"set after commit", []step{{engine, []any{"a"}}, {commit, nil}, {app, []any{"a"}}}, ctxAlreadyUsed},
		{"commit twice", []step{{engine, []any{"a"}}, {commit, nil}, {commit, nil}}, ctxAlreadyUsed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newAdmissionHarness(t, 2)
			l := newSandboxListener(t)
			cc := newContextClient(t, h)
			cc.create(t, l.listen, l.closeR)
			for _, s := range tc.steps {
				cc.request(t, s.op, s.arg...)
			}
			expectProtocolError(t, cc.c, cc.ctx, tc.code)
			l.handOver()
		})
	}
}

func TestSecurityContextInvalidListenFD(t *testing.T) {
	h := newAdmissionHarness(t, 2)
	// A connected socket, a regular pipe and a datagram socket listen on nothing.
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pair[1])
	dgram, err := unix.Socket(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(dgram)
	pipe := make([]int, 2)
	if err := unix.Pipe2(pipe, unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pipe[1])
	for name, fd := range map[string]int{"connected": pair[0], "datagram": dgram, "pipe": pipe[0]} {
		t.Run(name, func(t *testing.T) {
			cc := newContextClient(t, h)
			r, w := mustPipe(t)
			defer unix.Close(w)
			cc.create(t, fd, r)
			expectProtocolError(t, cc.c, cc.manager, uint32(securitycontext.WpSecurityContextManagerV1ErrorInvalidListenFd))
			_ = unix.Close(r)
		})
	}
}

func mustPipe(t *testing.T) (r, w int) {
	t.Helper()
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	return p[0], p[1]
}

// A sandboxed client may bind the manager (binding every global is common)
// but create_listener is the nested error.
func TestSecurityContextNestedForbidden(t *testing.T) {
	h := newAdmissionHarness(t, 2)
	l := newSandboxListener(t)
	newContextClient(t, h).commit(t, l, "org.flatpak", "")
	c := l.connect(t)
	manager := bindProtocol(t, c, "wp_security_context_manager_v1")
	waitTrue(t, "tagged", func() bool { return h.sandboxedCount() == 1 })
	inner := newSandboxListener(t)
	id := c.AllocateID()
	if err := wireRequest(c, manager, uint16(securitycontext.WpSecurityContextManagerV1RequestCreateListener), []int{inner.listen, inner.closeR}, id); err != nil {
		t.Fatal(err)
	}
	expectProtocolError(t, c, manager, uint32(securitycontext.WpSecurityContextManagerV1ErrorNested))
	// The compositor closed its copies: nothing listens on the inner socket
	// once ours goes too.
	inner.handOver()
	waitTrue(t, "inner closed", inner.refused)
}
