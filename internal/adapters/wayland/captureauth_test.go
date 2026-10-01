package wayland

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	source "github.com/bnema/go-wayland-bindings/server/extimagecapturesource"
	ext "github.com/bnema/go-wayland-bindings/server/extimagecopycapture"
	"github.com/bnema/neferwl/internal/adapters/captureallow"
	"github.com/bnema/neferwl/internal/adapters/wayland/imagecapture"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

func allowStore(t *testing.T, content string) *captureallow.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capture-allow")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return captureallow.NewStoreOwnedBy(path, uint32(os.Getuid()), zerowrap.Default())
}

func authHarness(t *testing.T, store *captureallow.Store, peer peerExe) (*admissionHarness, *lockedBuffer) {
	t.Helper()
	var out lockedBuffer
	log := zerowrap.New(zerowrap.Config{Level: "debug", Format: "json", Output: &out}).WithField("component", "wayland")
	return newAdmissionHarnessWith(t, maxCaptureInflight+2, Options{CaptureAllow: store, peer: peer}, log), &out
}

// The executable behind a real client connection from this very process is
// the test binary: a list naming it lets the client capture, one naming
// something else does not. No test double for the peer lookup.
func TestCaptureAllowlistRealPeer(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h, out := authHarness(t, allowStore(t, "# mine\n"+self+"\n"), nil)
	a := h.client(t)
	if _, ok := h.accepted(t, a.captureWlr(t)); !ok {
		t.Fatal("listed executable refused")
	}
	if strings.Contains(out.String(), `"message":"capture refused"`) {
		t.Fatalf("log = %s", out.String())
	}

	h2, out2 := authHarness(t, allowStore(t, "/usr/bin/grim\n"), nil)
	b := h2.client(t)
	for i := 0; i < 3; i++ {
		if _, ok := h2.accepted(t, b.captureWlr(t)); ok {
			t.Fatal("unlisted executable captured")
		}
	}
	log := out2.String()
	if n := strings.Count(log, `"message":"capture refused"`); n != 1 {
		t.Fatalf("refusal logged %d times, want once per client: %s", n, log)
	}
	for _, want := range []string{`"component":"wayland"`, `"exe":"` + self + `"`, `"pid":` + strconv.Itoa(os.Getpid())} {
		if !strings.Contains(log, want) {
			t.Fatalf("log lacks %s: %s", want, log)
		}
	}
}

// Every capture path asks: wlr-screencopy frames and ext sessions. A refused
// ext session is stopped at once, before any constraint.
func TestCaptureAllowlistEveryPath(t *testing.T) {
	h, _ := authHarness(t, allowStore(t, "/usr/bin/grim\n"), nil)
	a := h.client(t)
	if _, ok := h.accepted(t, a.captureWlr(t)); ok {
		t.Fatal("wlr capture allowed")
	}
	if !a.extSessionStopped(t) {
		t.Fatal("ext session not refused")
	}
}

// extSessionStopped opens an ext capture session and reports whether the
// compositor stopped it at once, as it does for a client that may not capture.
func (a *admissionClient) extSessionStopped(t *testing.T) bool {
	t.Helper()
	c := a.c
	sourceManager := bindProtocol(t, c, "ext_output_image_capture_source_manager_v1")
	manager := bindProtocol(t, c, "ext_image_copy_capture_manager_v1")
	src, session := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, src)
	requestProtocol(t, c, sourceManager, source.ExtOutputImageCaptureSourceManagerV1RequestCreateSource, src, a.output)
	events := &captureEvents{events: make(chan uint16, 16)}
	events.SetID(session)
	registerWireProxy(c, events)
	requestProtocol(t, c, manager, ext.ExtImageCopyCaptureManagerV1RequestCreateSession, session, src, uint32(0))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return <-events.events == uint16(ext.ExtImageCopyCaptureSessionV1EventStopped)
}

func TestCaptureAllowlistModes(t *testing.T) {
	self, _ := os.Executable()
	tests := []struct {
		name, content string
		want          bool
	}{
		{"star", "*\n", true},
		{"listed", self + "\n", true},
		{"other", "/usr/bin/grim\n", false},
		{"empty", "", false},
		{"invalid", self + "\nnot-absolute\n", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := authHarness(t, allowStore(t, tc.content), nil)
			a := h.client(t)
			_, ok := h.accepted(t, a.captureWlr(t))
			if ok != tc.want {
				t.Fatalf("accepted = %v, want %v", ok, tc.want)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		store := captureallow.NewStoreOwnedBy(filepath.Join(t.TempDir(), "absent"), uint32(os.Getuid()), zerowrap.Default())
		h, _ := authHarness(t, store, nil)
		if _, ok := h.accepted(t, h.client(t).captureWlr(t)); ok {
			t.Fatal("missing file: the built-in list must refuse a client it does not list")
		}
	})
	t.Run("no allowlist", func(t *testing.T) {
		h, _ := authHarness(t, nil, nil)
		if _, ok := h.accepted(t, h.client(t).captureWlr(t)); ok {
			t.Fatal("capture allowed without an allowlist: it must fail closed")
		}
	})
}

// devNull is a stand-in pidfd the test can see closed.
func devNull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// With a peer lookup mock: the pidfd is taken once per client and closed with
// it; the executable is read again at every wlr frame request and every ext
// session creation, but not at the frames of an ext session.
func TestCaptureAllowlistResolvesEachRequest(t *testing.T) {
	peer := newMockpeerExe(t)
	pidfd := devNull(t)
	peer.EXPECT().Pidfd(mock.Anything).RunAndReturn(func(fd int) (*os.File, error) {
		if fd < 0 {
			t.Errorf("Pidfd(%d)", fd)
		}
		return pidfd, nil
	}).Once()
	var exes atomic.Int32
	peer.EXPECT().Exe(pidfd).RunAndReturn(func(*os.File) (string, int, error) {
		exes.Add(1)
		return "/usr/bin/grim", 4242, nil
	})
	h, out := authHarness(t, allowStore(t, "/usr/bin/grim\n"), peer)
	a := h.client(t)
	for i := 0; i < 3; i++ {
		req, ok := h.accepted(t, a.captureWlr(t))
		if !ok {
			t.Fatal("listed executable refused")
		}
		h.complete(t, req, h.inflight(t)-1)
	}
	if n := exes.Load(); n != 3 {
		t.Fatalf("resolved %d times for 3 wlr requests", n)
	}
	// One ext session (one resolution), then frames that do not resolve.
	p := a.captureExt(t)
	if n := exes.Load(); n != 4 {
		t.Fatalf("resolved %d times after the ext session", n)
	}
	req, ok := h.accepted(t, p)
	if !ok {
		t.Fatal("ext frame refused")
	}
	h.complete(t, req, 0)
	a.destroy(t, p)
	req, ok = h.accepted(t, a.captureExt(t))
	if !ok {
		t.Fatal("second ext frame refused")
	}
	h.complete(t, req, 0)
	if n := exes.Load(); n != 4 {
		t.Fatalf("resolved %d times, ext frames must reuse the session's", n)
	}
	if strings.Contains(out.String(), `"message":"capture refused"`) {
		t.Fatalf("log = %s", out.String())
	}
	peers := func() int {
		n := -1
		h.s.display.Do(func() { n = len(h.s.peers) })
		return n
	}
	if peers() != 1 {
		t.Fatalf("peers = %d", peers())
	}
	_ = a.c.Close()
	deadline := time.Now().Add(2 * time.Second)
	for peers() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("peer entry kept after the client went")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := pidfd.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("pidfd not closed with the client: %v", err)
	}
}

// A program that execs another binary after connecting is judged by the new
// one at its next request; the refusal is logged once per distinct executable.
func TestCaptureAllowlistExecAfterConnect(t *testing.T) {
	peer := newMockpeerExe(t)
	pidfd := devNull(t)
	peer.EXPECT().Pidfd(mock.Anything).Return(pidfd, nil).Once()
	seq := []string{"/usr/bin/grim", "/usr/bin/evil", "/usr/bin/evil", "/usr/bin/grim", "/usr/bin/evil", "/usr/bin/evil2"}
	i := 0
	peer.EXPECT().Exe(pidfd).RunAndReturn(func(*os.File) (string, int, error) {
		e := seq[i]
		i++
		return e, 4242, nil
	})
	h, out := authHarness(t, allowStore(t, "/usr/bin/grim\n"), peer)
	a := h.client(t)
	for n, want := range []bool{true, false, false, true, false, false} {
		req, ok := h.accepted(t, a.captureWlr(t))
		if ok != want {
			t.Fatalf("request %d (%s): accepted = %v", n, seq[n], ok)
		}
		if ok {
			h.complete(t, req, 0)
		}
	}
	log := out.String()
	if c := strings.Count(log, `"message":"capture refused"`); c != 2 || !strings.Contains(log, `"exe":"/usr/bin/evil"`) || !strings.Contains(log, `"exe":"/usr/bin/evil2"`) {
		t.Fatalf("want one refusal per distinct executable: %s", log)
	}
}

// Without SO_PEERPIDFD a list policy identifies nobody: fail closed, one Warn.
func TestCaptureAllowlistNoPidfdFailsClosed(t *testing.T) {
	h, out := authHarness(t, allowStore(t, "/usr/bin/grim\n"), linuxPeerExe{noPidfd: true})
	a := h.client(t)
	for i := 0; i < 3; i++ {
		if _, ok := h.accepted(t, a.captureWlr(t)); ok {
			t.Fatal("captured without a pidfd")
		}
	}
	log := out.String()
	if n := strings.Count(log, `"message":"kernel has no SO_PEERPIDFD`); n != 1 || !strings.Contains(log, `"level":"warn"`) {
		t.Fatalf("want one warning: %s", log)
	}
	// `*` needs no identity.
	h2, _ := authHarness(t, allowStore(t, "*\n"), linuxPeerExe{noPidfd: true})
	if _, ok := h2.accepted(t, h2.client(t).captureWlr(t)); !ok {
		t.Fatal("`*` must not need a pidfd")
	}
}

func TestCaptureAllowlistUnresolvedExecutableRefused(t *testing.T) {
	for name, err := range map[string]error{"deleted": errExeDeleted, "unknown": errors.New("no such process")} {
		t.Run(name, func(t *testing.T) {
			peer := newMockpeerExe(t)
			pidfd := devNull(t)
			peer.EXPECT().Pidfd(mock.Anything).Return(pidfd, nil).Once()
			peer.EXPECT().Exe(pidfd).Return("", 77, err).Times(2)
			h, out := authHarness(t, allowStore(t, "/usr/bin/grim\n"), peer)
			a := h.client(t)
			for i := 0; i < 2; i++ {
				if _, ok := h.accepted(t, a.captureWlr(t)); ok {
					t.Fatal("unresolved executable captured")
				}
			}
			log := out.String()
			if strings.Count(log, `"message":"capture refused"`) != 1 || !strings.Contains(log, `"pid":77`) || !strings.Contains(log, err.Error()) {
				t.Fatalf("log = %s", log)
			}
		})
	}
}

// The check is off for `*`: the executable is never looked up.
func TestCaptureAllowlistDisabledSkipsLookup(t *testing.T) {
	peer := newMockpeerExe(t) // any call fails the test
	h, _ := authHarness(t, allowStore(t, "*\n"), peer)
	if _, ok := h.accepted(t, h.client(t).captureWlr(t)); !ok {
		t.Fatal("refused")
	}
}

func TestCaptureAllowlistReloadApplies(t *testing.T) {
	self, _ := os.Executable()
	path := filepath.Join(t.TempDir(), "capture-allow")
	if err := os.WriteFile(path, []byte("/usr/bin/grim\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := captureallow.NewStoreOwnedBy(path, uint32(os.Getuid()), zerowrap.Default())
	h, _ := authHarness(t, store, nil)
	a := h.client(t)
	if _, ok := h.accepted(t, a.captureWlr(t)); ok {
		t.Fatal("captured before listing")
	}
	// Store.Run is exercised in its own package; here the swap is direct.
	if err := os.WriteFile(path, []byte(self+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	go func() { _ = store.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if req, ok := h.accepted(t, a.captureWlr(t)); ok {
			h.complete(t, req, 0)
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("reload never applied")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The real lookup, against a socket whose peer is this process.
func TestLinuxPeerExeSelf(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	var l linuxPeerExe
	pidfd, err := l.Pidfd(fds[0])
	if errors.Is(err, errNoPidfd) {
		t.Skip("kernel without SO_PEERPIDFD")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer pidfd.Close()
	exe, pid, err := l.Exe(pidfd)
	if err != nil {
		t.Fatal(err)
	}
	if exe != self || pid != os.Getpid() {
		t.Fatalf("Exe = %q, %d; want %q, %d", exe, pid, self, os.Getpid())
	}
	if _, err := (linuxPeerExe{noPidfd: true}).Pidfd(fds[0]); !errors.Is(err, errNoPidfd) {
		t.Fatalf("noPidfd: %v", err)
	}
}

// A process whose binary was removed is refused, and a peer that already
// exited is never resolved from its (possibly recycled) pid.
func TestLinuxPeerExeDeletedAndExited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "child")
	src, err := os.ReadFile("/bin/sleep")
	if err != nil {
		t.Skip("no /bin/sleep")
	}
	if err := os.WriteFile(path, src, 0o755); err != nil {
		t.Fatal(err)
	}
	proc, err := os.StartProcess(path, []string{path, "30"}, &os.ProcAttr{})
	if err != nil {
		t.Skip("cannot run a copy of sleep: ", err)
	}
	defer func() { _ = proc.Kill(); _, _ = proc.Wait() }()
	_ = os.Remove(path)
	// Wait until exec has completed so /proc/<pid>/exe is the copy.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := readExe(proc.Pid); errors.Is(err, errExeDeleted) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deleted executable not refused")
		}
		time.Sleep(5 * time.Millisecond)
	}
	pidfd, err := unix.PidfdOpen(proc.Pid, 0)
	if err != nil {
		t.Skip("pidfd_open: ", err)
	}
	defer unix.Close(pidfd)
	if _, _, err := exeOfPidfd(pidfd); !errors.Is(err, errExeDeleted) {
		t.Fatalf("deleted: %v", err)
	}
	_ = proc.Kill()
	_, _ = proc.Wait()
	if exe, _, err := exeOfPidfd(pidfd); err == nil {
		t.Fatalf("exited peer resolved to %q", exe)
	}
}

// The exclusion attach asks too: a HUD whose executable is not listed gets
// `unauthorized` even with the right token, while the recorder that owns the
// exclusion (listed) is unaffected.
func TestCaptureAllowlistExclusionAttach(t *testing.T) {
	peer := newMockpeerExe(t)
	// The recorder connects first, the HUD second.
	p1, p2 := devNull(t), devNull(t)
	peer.EXPECT().Pidfd(mock.Anything).Return(p1, nil).Once()
	peer.EXPECT().Pidfd(mock.Anything).Return(p2, nil).Once()
	peer.EXPECT().Exe(p1).Return("/usr/bin/nefercap", 11, nil)
	peer.EXPECT().Exe(p2).Return("/usr/bin/other", 12, nil)
	h := newCaptureHarnessWith(t, func(o *Options, _ *Channels) {
		o.CaptureAllow, o.peer = allowStore(t, "/usr/bin/nefercap\n"), peer
	})
	_, _, _, _, token := h.exclusionOwner(t)
	hd := h.hud(t, ports.LayerOverlay, 2)
	att := hd.attach(t, token)
	expectAttachment(t, hd.c, att, imagecapture.NeferwlCaptureLayerV1EventFailed, int64(imagecapture.NeferwlCaptureLayerV1FailureUnauthorized))
}

// An ext frame uses the client's latest resolution: after a later wlr request
// failed to resolve the executable, the frame is refused as unknown, not as
// "not in allowlist" with an empty path.
func TestCaptureAllowlistFrameUnknownExecutable(t *testing.T) {
	peer := newMockpeerExe(t)
	pidfd := devNull(t)
	peer.EXPECT().Pidfd(mock.Anything).Return(pidfd, nil).Once()
	var calls atomic.Int32
	peer.EXPECT().Exe(pidfd).RunAndReturn(func(*os.File) (string, int, error) {
		if calls.Add(1) == 2 {
			return "", 55, errors.New("peer exited")
		}
		return "/usr/bin/grim", 55, nil
	})
	h, out := authHarness(t, allowStore(t, "/usr/bin/grim\n"), peer)
	a := h.client(t)
	a.extMgr, a.session, _ = captureTestExt(t, a.c, a.output) // resolution 1: listed
	if _, ok := h.accepted(t, a.captureWlr(t)); ok {          // resolution 2: fails
		t.Fatal("unresolved wlr request captured")
	}
	frame, ev := a.newFrame()
	requestProtocol(t, a.c, a.session, ext.ExtImageCopyCaptureSessionV1RequestCreateFrame, frame)
	requestProtocol(t, a.c, frame, ext.ExtImageCopyCaptureFrameV1RequestAttachBuffer, a.buf)
	requestProtocol(t, a.c, frame, ext.ExtImageCopyCaptureFrameV1RequestCapture)
	if err := a.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.accepted(t, pendingCapture{frame: frame, events: ev, failed: uint16(ext.ExtImageCopyCaptureFrameV1EventFailed), ext: true}); ok {
		t.Fatal("ext frame captured after a failed resolution")
	}
	if !strings.Contains(out.String(), `"reason":"executable unknown: peer exited"`) {
		t.Fatalf("log = %s", out.String())
	}
}
