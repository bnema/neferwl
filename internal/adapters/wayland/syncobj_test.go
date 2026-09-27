package wayland

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/bnema/neferwl/internal/logging"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/linuxdmabuf"
	"github.com/bnema/purego-libwayland/protocol/linuxdrmsyncobj"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/wlturbo"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

func TestSyncobjStructSizes(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  uintptr
		req  uintptr
	}{
		{"destroy", unsafe.Sizeof(syncobjDestroy{}), ioctlSyncobjDestroy},
		{"handle_to_fd", unsafe.Sizeof(syncobjHandle{}), ioctlSyncobjHandleToFD},
		{"fd_to_handle", unsafe.Sizeof(syncobjHandle{}), ioctlSyncobjFDToHandle},
		{"timeline_signal", unsafe.Sizeof(syncobjTimelineArray{}), ioctlSyncobjTimelineSignal},
		{"eventfd", unsafe.Sizeof(syncobjEventfd{}), ioctlSyncobjEventfd},
		{"get_cap", unsafe.Sizeof(drmCap{}), ioctlGetCap},
	} {
		if want := (tc.req >> 16) & 0x3fff; tc.got != want {
			t.Errorf("%s: size %d, ioctl encodes %d", tc.name, tc.got, want)
		}
	}
}

// syncHarness is a server with a mocked syncobj device and a client with
// a mapped window, a dmabuf buffer and the syncobj objects.
type syncHarness struct {
	t         *testing.T
	s         *Server
	c         *wlturbo.Display
	dev       *mocksyncobjDevice
	contents  chan ports.SurfaceContent
	presented chan ports.OutputPresented
	surf      uint32
	syncSurf  uint32
	timeline  uint32
	win       ports.WindowID
	mu        sync.Mutex
	efds      map[uint64]int // eventfd by acquire point
	signals   []uint64
	fences    []*os.File
	releases  map[uint32]*syncReleaseProxy
	mgr       uint32
}

func newSyncHarness(t *testing.T) *syncHarness {
	h := &syncHarness{t: t, efds: map[uint64]int{}}
	h.dev = newMocksyncobjDevice(t)
	h.dev.EXPECT().fdToHandle(mock.Anything).Return(7, nil).Maybe()
	h.dev.EXPECT().destroy(mock.Anything).Return(nil).Maybe()
	h.dev.EXPECT().eventfd(uint32(7), mock.Anything, mock.Anything).RunAndReturn(func(_ uint32, point uint64, efd int) error {
		// Keep a duplicate: the waiter closes its own when it fires.
		dup, err := unix.Dup(efd)
		if err != nil {
			return err
		}
		h.mu.Lock()
		h.efds[point] = dup
		h.mu.Unlock()
		return nil
	}).Maybe()
	h.dev.EXPECT().exportSyncFile(uint32(7), mock.Anything).RunAndReturn(func(uint32, uint64) (*os.File, error) {
		r, w, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		w.Close()
		h.mu.Lock()
		h.fences = append(h.fences, r)
		h.mu.Unlock()
		return r, nil
	}).Maybe()
	h.dev.EXPECT().signal(uint32(7), mock.Anything).RunAndReturn(func(_ uint32, point uint64) error {
		h.mu.Lock()
		h.signals = append(h.signals, point)
		h.mu.Unlock()
		return nil
	}).Maybe()
	dir := t.TempDir()
	events := make(chan ports.ClientEvent, 16)
	commands := make(chan ports.ClientCommand, 16)
	h.contents = make(chan ports.SurfaceContent, 64)
	h.presented = make(chan ports.OutputPresented, 16)
	sup := ports.DMABufSupport{Formats: []ports.DMABufFormat{linearARGB}}
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs, DMABuf: sup, syncDev: h.dev}, Channels{Events: events, Commands: commands, Contents: h.contents, Presented: h.presented}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		h.mu.Lock()
		for _, fd := range h.efds {
			unix.Close(fd)
		}
		h.mu.Unlock()
	})
	h.s = s
	h.c = protocolClient(t, s, dir)
	w, surf, xdg := surfaceMapper(t, h.c, events)()
	registerProtocol(t, h.c, xdg)
	h.win, h.surf = w.ID, surf
	commands <- ports.ConfigureWindow{ID: w.ID, Width: 64, Height: 16, Output: "HEADLESS-1"}
	mgr := bindProtocol(t, h.c, "wp_linux_drm_syncobj_manager_v1")
	h.syncSurf = h.c.AllocateID()
	requestProtocol(t, h.c, mgr, linuxdrmsyncobj.WpLinuxDrmSyncobjManagerV1RequestGetSurface, h.syncSurf, surf)
	registerProtocol(t, h.c, h.syncSurf)
	fd, err := unix.MemfdCreate("timeline", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	h.timeline = h.c.AllocateID()
	registerProtocol(t, h.c, h.timeline)
	if err := h.c.SendRequestWithFDs(mgr, uint16(linuxdrmsyncobj.WpLinuxDrmSyncobjManagerV1RequestImportTimeline), []int{fd}, h.timeline); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); ; {
		var out string
		s.display.Do(func() { out = s.frameOutput(s.windows[w.ID].xdg.surface) })
		if out != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("configure not applied")
		}
		time.Sleep(2 * time.Millisecond)
	}
	return h
}

// dmabuf makes a client dmabuf wl_buffer (a memfd stands in for it).
func (h *syncHarness) dmabuf() uint32 {
	t, c := h.t, h.c
	dm := bindVersion(t, c, "zwp_linux_dmabuf_v1", dmabufVersion)
	fd, err := unix.MemfdCreate("buf", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, 256*16); err != nil {
		t.Fatal(err)
	}
	p := c.AllocateID()
	requestProtocol(t, c, dm, linuxdmabuf.ZwpLinuxDmabufV1RequestCreateParams, p)
	registerProtocol(t, c, p)
	if err := c.SendRequestWithFDs(p, uint16(linuxdmabuf.ZwpLinuxBufferParamsV1RequestAdd), []int{fd}, uint32(0), uint32(0), uint32(256), uint32(0), uint32(0)); err != nil {
		t.Fatal(err)
	}
	b := c.AllocateID()
	rel := &syncReleaseProxy{released: make(chan struct{}, 4)}
	rel.SetID(b)
	c.Context().Register(rel)
	requestProtocol(t, c, p, linuxdmabuf.ZwpLinuxBufferParamsV1RequestCreateImmed, b, int32(64), int32(16), linearARGB.Format, uint32(0))
	h.releases[b] = rel
	return b
}

type syncReleaseProxy struct {
	wlturbo.BaseProxy
	released chan struct{}
}

func (p *syncReleaseProxy) Dispatch(e *wlturbo.Event) {
	if uint32(e.Opcode) == wayland.BufferEventRelease {
		p.released <- struct{}{}
	}
}

func (h *syncHarness) setPoints(acquire, release uint64) {
	requestProtocol(h.t, h.c, h.syncSurf, linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1RequestSetAcquirePoint, h.timeline, uint32(acquire>>32), uint32(acquire))
	requestProtocol(h.t, h.c, h.syncSurf, linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1RequestSetReleasePoint, h.timeline, uint32(release>>32), uint32(release))
}

func (h *syncHarness) commit(buf uint32, acquire, release uint64) {
	t, c := h.t, h.c
	requestProtocol(t, c, h.surf, wayland.SurfaceRequestAttach, buf, int32(0), int32(0))
	h.setPoints(acquire, release)
	requestProtocol(t, c, h.surf, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
}

// content waits for a DMABuf content of the window.
func (h *syncHarness) content(wait time.Duration) (ports.SurfaceContent, bool) {
	deadline := time.After(wait)
	for {
		select {
		case c := <-h.contents:
			if c.ID == h.win && c.DMABuf != nil {
				return c, true
			}
		case <-deadline:
			return ports.SurfaceContent{}, false
		}
	}
}

// fire makes the kernel's eventfd for an acquire point readable.
func (h *syncHarness) fire(point uint64) {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.mu.Lock()
		fd, ok := h.efds[point]
		h.mu.Unlock()
		if ok {
			var one [8]byte
			one[0] = 1
			if _, err := unix.Write(fd, one[:]); err != nil {
				h.t.Fatal(err)
			}
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("no eventfd for point %d", point)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (h *syncHarness) signalled() []uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]uint64(nil), h.signals...)
}

// A commit waits for its acquire point; the content carries the fence;
// replacing it signals the release point once outputs moved on, and
// wl_buffer.release is never sent. The acquire fence closes only then.
func TestSyncobjAcquireAndRelease(t *testing.T) {
	h := newSyncHarness(t)
	h.releases = map[uint32]*syncReleaseProxy{}
	a, b := h.dmabuf(), h.dmabuf()
	h.commit(a, 1, 2)
	if _, ok := h.content(50 * time.Millisecond); ok {
		t.Fatal("content before the acquire point had a fence")
	}
	h.fire(1)
	first, ok := h.content(2 * time.Second)
	if !ok || first.Acquire == nil {
		t.Fatalf("content %+v", first)
	}
	h.commit(b, 3, 4)
	h.fire(3)
	second, ok := h.content(2 * time.Second)
	if !ok {
		t.Fatal("second content")
	}
	if _, err := first.Acquire.Stat(); err != nil {
		t.Fatal("acquire fence closed before release")
	}
	// The output read the second content: the first buffer is released.
	h.presented <- ports.OutputPresented{Output: "HEADLESS-1", Seen: map[ports.WindowID]uint64{h.win: second.Seq}}
	deadline := time.Now().Add(2 * time.Second)
	for len(h.signalled()) == 0 && time.Now().Before(deadline) {
		if err := h.c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := h.signalled(); len(got) != 1 || got[0] != 2 {
		t.Fatalf("signalled %v", got)
	}
	if _, err := first.Acquire.Stat(); err == nil {
		t.Fatal("acquire fence left open after release")
	}
	if err := h.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.releases[a].released:
		t.Fatal("wl_buffer.release sent under explicit sync")
	default:
	}
}

func TestSyncobjProtocolErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(h *syncHarness)
		obj  func(h *syncHarness) uint32
		code uint32
	}{
		{"conflicting points", func(h *syncHarness) {
			b := h.dmabuf()
			requestProtocol(t, h.c, h.surf, wayland.SurfaceRequestAttach, b, int32(0), int32(0))
			requestProtocol(t, h.c, h.syncSurf, linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1RequestSetAcquirePoint, h.timeline, uint32(0), uint32(5))
			requestProtocol(t, h.c, h.syncSurf, linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1RequestSetReleasePoint, h.timeline, uint32(0), uint32(5))
			requestProtocol(t, h.c, h.surf, wayland.SurfaceRequestCommit)
		}, func(h *syncHarness) uint32 { return h.syncSurf }, uint32(linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1ErrorConflictingPoints)},
		{"no release point", func(h *syncHarness) {
			b := h.dmabuf()
			requestProtocol(t, h.c, h.surf, wayland.SurfaceRequestAttach, b, int32(0), int32(0))
			requestProtocol(t, h.c, h.syncSurf, linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1RequestSetAcquirePoint, h.timeline, uint32(0), uint32(5))
			requestProtocol(t, h.c, h.surf, wayland.SurfaceRequestCommit)
		}, func(h *syncHarness) uint32 { return h.syncSurf }, uint32(linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1ErrorNoReleasePoint)},
		{"buffer without points", func(h *syncHarness) {
			b := h.dmabuf()
			requestProtocol(t, h.c, h.surf, wayland.SurfaceRequestAttach, b, int32(0), int32(0))
			requestProtocol(t, h.c, h.surf, wayland.SurfaceRequestCommit)
		}, func(h *syncHarness) uint32 { return h.syncSurf }, uint32(linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1ErrorNoAcquirePoint)},
		{"no acquire point", func(h *syncHarness) {
			b := h.dmabuf()
			requestProtocol(t, h.c, h.surf, wayland.SurfaceRequestAttach, b, int32(0), int32(0))
			requestProtocol(t, h.c, h.syncSurf, linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1RequestSetReleasePoint, h.timeline, uint32(0), uint32(5))
			requestProtocol(t, h.c, h.surf, wayland.SurfaceRequestCommit)
		}, func(h *syncHarness) uint32 { return h.syncSurf }, uint32(linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1ErrorNoAcquirePoint)},
		{"points without attach", func(h *syncHarness) {
			b := h.dmabuf()
			h.commit(b, 1, 2)
			h.fire(1)
			h.setPoints(3, 4)
			requestProtocol(t, h.c, h.surf, wayland.SurfaceRequestCommit)
		}, func(h *syncHarness) uint32 { return h.syncSurf }, uint32(linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1ErrorNoBuffer)},
		{"points with null attach", func(h *syncHarness) {
			requestProtocol(t, h.c, h.surf, wayland.SurfaceRequestAttach, uint32(0), int32(0), int32(0))
			h.setPoints(1, 2)
			requestProtocol(t, h.c, h.surf, wayland.SurfaceRequestCommit)
		}, func(h *syncHarness) uint32 { return h.syncSurf }, uint32(linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1ErrorNoBuffer)},
		{"wl_shm buffer", func(h *syncHarness) {
			b := shmBuffer(t, h.c)
			requestProtocol(t, h.c, h.surf, wayland.SurfaceRequestAttach, b, int32(0), int32(0))
			h.setPoints(1, 2)
			requestProtocol(t, h.c, h.surf, wayland.SurfaceRequestCommit)
		}, func(h *syncHarness) uint32 { return h.syncSurf }, uint32(linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1ErrorUnsupportedBuffer)},
		{"surface destroyed", func(h *syncHarness) {
			requestProtocol(t, h.c, h.surf, wayland.SurfaceRequestDestroy)
			h.setPoints(1, 2)
		}, func(h *syncHarness) uint32 { return h.syncSurf }, uint32(linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1ErrorNoSurface)},
		{"surface exists", func(h *syncHarness) {
			mgr := bindProtocol(t, h.c, "wp_linux_drm_syncobj_manager_v1")
			h.mgr = mgr
			requestProtocol(t, h.c, mgr, linuxdrmsyncobj.WpLinuxDrmSyncobjManagerV1RequestGetSurface, h.c.AllocateID(), h.surf)
		}, func(h *syncHarness) uint32 { return h.mgr }, uint32(linuxdrmsyncobj.WpLinuxDrmSyncobjManagerV1ErrorSurfaceExists)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSyncHarness(t)
			h.releases = map[uint32]*syncReleaseProxy{}
			h.t = t
			tc.run(h)
			expectProtocolError(t, h.c, tc.obj(h), tc.code)
		})
	}
}

// An invalid timeline fd is a protocol error.
func TestSyncobjInvalidTimeline(t *testing.T) {
	dev := newMocksyncobjDevice(t)
	dev.EXPECT().fdToHandle(mock.Anything).Return(0, unix.EINVAL).Once()
	dir := t.TempDir()
	s, err := New(Options{RuntimeDir: dir, Outputs: testOutputs, syncDev: dev}, Channels{}, logging.For(context.Background(), "wayland"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	c := protocolClient(t, s, dir)
	mgr := bindProtocol(t, c, "wp_linux_drm_syncobj_manager_v1")
	fd, _ := unix.MemfdCreate("bad", 0)
	defer unix.Close(fd)
	if err := c.SendRequestWithFDs(mgr, uint16(linuxdrmsyncobj.WpLinuxDrmSyncobjManagerV1RequestImportTimeline), []int{fd}, c.AllocateID()); err != nil {
		t.Fatal(err)
	}
	expectProtocolError(t, c, mgr, uint32(linuxdrmsyncobj.WpLinuxDrmSyncobjManagerV1ErrorInvalidTimeline))
}

// A synchronized subsurface dropped before its acquire is ready must
// retire its release point without sending wl_buffer.release.
func TestSyncobjDroppedSubsurfaceCommit(t *testing.T) {
	h := newSyncHarness(t)
	h.releases = map[uint32]*syncReleaseProxy{}
	c := h.c
	comp := bindProtocol(t, c, "wl_compositor")
	subc := bindProtocol(t, c, "wl_subcompositor")
	mgr := bindProtocol(t, c, "wp_linux_drm_syncobj_manager_v1")
	child := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, child)
	registerProtocol(t, c, child)
	sub := c.AllocateID()
	requestProtocol(t, c, subc, wayland.SubcompositorRequestGetSubsurface, sub, child, h.surf)
	registerProtocol(t, c, sub)
	syncChild := c.AllocateID()
	requestProtocol(t, c, mgr, linuxdrmsyncobj.WpLinuxDrmSyncobjManagerV1RequestGetSurface, syncChild, child)
	registerProtocol(t, c, syncChild)
	b := h.dmabuf()
	requestProtocol(t, c, child, wayland.SurfaceRequestAttach, b, int32(0), int32(0))
	requestProtocol(t, c, syncChild, linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1RequestSetAcquirePoint, h.timeline, uint32(0), uint32(21))
	requestProtocol(t, c, syncChild, linuxdrmsyncobj.WpLinuxDrmSyncobjSurfaceV1RequestSetReleasePoint, h.timeline, uint32(0), uint32(22))
	requestProtocol(t, c, child, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, sub, wayland.SubsurfaceRequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	h.fire(21) // dropping before acquire means the later signal must not release
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got := h.signalled(); len(got) != 0 {
		t.Fatalf("premature release point: %v", got)
	}
	select {
	case <-h.releases[b].released:
		t.Fatal("explicit sync sent wl_buffer.release")
	default:
	}
}

// A commit dropped before its acquire point has a fence signals nothing
// (the client has not submitted that point) and sends no wl_buffer.release;
// the waiter closes its eventfd and no acquire file is left open.
func TestSyncobjDroppedCommit(t *testing.T) {
	h := newSyncHarness(t)
	h.releases = map[uint32]*syncReleaseProxy{}
	a := h.dmabuf()
	before := openFDs(t)
	h.commit(a, 1, 2)
	requestProtocol(t, h.c, h.surf, wayland.SurfaceRequestDestroy)
	if err := h.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	// The point never gets a fence: the waiter must still stop polling it.
	time.Sleep(250 * time.Millisecond)
	if err := h.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	if got := h.signalled(); len(got) != 0 {
		t.Fatalf("signalled %v for a commit whose acquire point never arrived", got)
	}
	select {
	case <-h.releases[a].released:
		t.Fatal("wl_buffer.release sent under explicit sync")
	default:
	}
	// The harness keeps its own duplicate of the eventfd.
	if after := openFDs(t); after > before+1 {
		t.Fatalf("eventfds and sync files %d -> %d", before, after)
	}
}

// A commit dropped after its acquire point fired signals its release point.
func TestSyncobjDroppedReadyCommitReleases(t *testing.T) {
	h := newSyncHarness(t)
	h.releases = map[uint32]*syncReleaseProxy{}
	a := h.dmabuf()
	h.commit(a, 1, 2)
	h.fire(1)
	if _, ok := h.content(2 * time.Second); !ok {
		t.Fatal("content")
	}
	requestProtocol(t, h.c, h.surf, wayland.SurfaceRequestDestroy)
	deadline := time.Now().Add(2 * time.Second)
	for len(h.signalled()) == 0 && time.Now().Before(deadline) {
		if err := h.c.Roundtrip(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := h.signalled(); len(got) != 1 || got[0] != 2 {
		t.Fatalf("signalled %v", got)
	}
}

// openFDs counts the eventfds and sync files (pipes stand in for them)
// the process holds.
func openFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range ents {
		l, _ := os.Readlink("/proc/self/fd/" + e.Name())
		if strings.Contains(l, "eventfd") || strings.HasPrefix(l, "pipe:") {
			n++
		}
	}
	return n
}
