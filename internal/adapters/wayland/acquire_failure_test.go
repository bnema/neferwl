package wayland

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

func TestExplicitAcquireWatchFailureDiscardsCommit(t *testing.T) {
	for _, failure := range []string{"signalled", "eventfd"} {
		t.Run(failure, func(t *testing.T) {
			h := newSyncHarness(t)
			h.releases = map[uint32]*syncReleaseProxy{}
			// Order setup after import and reset on the display owner so no
			// concurrent device call can read the generated expectations.
			if err := h.c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			h.s.display.Do(func() { h.dev.ExpectedCalls = nil })
			h.dev.EXPECT().fdToHandle(mock.Anything).Return(7, nil).Maybe()
			h.dev.EXPECT().destroy(mock.Anything).Return(nil).Maybe()
			if failure == "signalled" {
				h.dev.EXPECT().signalled(uint32(7), uint64(101)).Return(false, unix.EIO).Once()
			} else {
				h.dev.EXPECT().signalled(uint32(7), uint64(101)).Return(false, nil).Once()
				h.dev.EXPECT().eventfd(uint32(7), uint64(101), mock.Anything).Return(unix.EIO).Once()
			}
			drainContents(h.contents)
			buffer := h.dmabuf()
			h.commit(buffer, 101, 102)
			if got, ok := h.content(50 * time.Millisecond); ok {
				t.Fatalf("untracked acquire published %+v", got)
			}
			h.s.display.Do(func() {
				surf := h.s.windows[h.win].xdg.surface
				if len(surf.queue) != 0 || surf.hold.acquire != nil {
					t.Error("failed acquire retained queued or rendered fence")
				}
				for _, tl := range h.s.timelines {
					if tl.uses != 0 {
						t.Errorf("timeline uses=%d", tl.uses)
					}
				}
			})
			if len(h.signalled()) != 0 {
				t.Fatal("unsafe release point signalled")
			}
			select {
			case <-h.releases[buffer].released:
				t.Fatal("explicit buffer release event")
			default:
			}
		})
	}
}

func TestImplicitAcquireWatchFailureDiscardsCommit(t *testing.T) {
	for _, failure := range []string{"poll", "hangup"} {
		t.Run(failure, func(t *testing.T) {
			h := newSyncHarness(t)
			h.releases = map[uint32]*syncReleaseProxy{}
			drainContents(h.contents)
			buffer := h.dmabuf()
			var plane *os.File
			var writer *os.File
			if failure == "poll" {
				plane = os.NewFile(uintptr(math.MaxInt32), "invalid")
			} else {
				var err error
				plane, writer, err = os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer plane.Close()
			}
			// Real pollable descriptors exercise observation errors without replacing
			// watch/poll functions or constructing a handwritten device double.
			if err := h.c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			h.s.display.Do(func() {
				var buf *dmabufBuffer
				var resource *wayland.Buffer
				for r, b := range h.s.buffers {
					if r.ID() == buffer {
						buf = b.(*dmabufBuffer)
						resource = wayland.WrapBuffer(r)
					}
				}
				if buf == nil {
					t.Error("buffer missing")
					return
				}
				original := buf.buf.Planes[0].File
				defer func() { buf.buf.Planes[0].File = original }()
				buf.buf.Planes[0].File = plane
				surf := h.s.windows[h.win].xdg.surface
				surf.Attach(surf.wl, resource, 0, 0)
				surf.Commit(surf.wl)
			})
			if writer != nil {
				writer.Close()
			}
			if got, ok := h.content(100 * time.Millisecond); ok {
				t.Fatalf("failed implicit acquire published %+v", got)
			}
			h.s.display.Do(func() {
				if len(h.s.windows[h.win].xdg.surface.queue) != 0 {
					t.Error("failed implicit acquire left queue blocked")
				}
			})
		})
	}
}

func TestAcquirePollTerminalFailureRetiresActiveAndAdded(t *testing.T) {
	poller := newMockacquirePoller(t)
	wake := make(chan struct{}, 1)
	sw, err := newSyncWaiter(func() { wake <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	sw.poller = poller
	defer sw.close()
	fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	active := sw.add(fd)
	var added *syncWait
	poller.EXPECT().poll(mock.Anything, 100).RunAndReturn(func(fds []unix.PollFd, _ int) (int, error) { fds[0].Revents = unix.POLLIN; return 1, nil }).Once()
	poller.EXPECT().poll(mock.Anything, 100).RunAndReturn(func([]unix.PollFd, int) (int, error) {
		fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC)
		if err != nil {
			t.Error(err)
			return -1, err
		}
		added = sw.add(fd)
		return -1, unix.EIO
	}).Once()
	sw.run(context.Background())
	select {
	case <-wake:
	default:
		t.Fatal("terminal failure did not wake owner")
	}
	for _, w := range []*syncWait{active, added} {
		if w == nil || !sw.fired(w) || !w.failed {
			t.Fatal("wait stranded after terminal poll failure")
		}
	}
	if !sw.stopped() {
		t.Fatal("waiter not terminal")
	}
	dev := newMocksyncobjDevice(t) // no device access permitted after terminal failure
	if w, err := sw.watch(syncPoint{tl: &timeline{dev: dev, handle: 7}, point: 1}); err == nil || !w.failed {
		t.Fatal("new explicit watch accepted")
	}
	var waits [4]*syncWait
	if err := sw.watchImplicit(&ports.DMABuf{}, &waits); err == nil {
		t.Fatal("new implicit watch accepted")
	}
}

func TestFailedRootRetiresEveryChildPredecessorGraph(t *testing.T) {
	h := newSyncHarness(t)
	sw := h.s.syncWait
	root := &surface{server: h.s}
	child := &surface{server: h.s}
	a, b := &surface{server: h.s}, &surface{server: h.s}
	tl := &timeline{dev: h.dev, handle: 7, alive: true, uses: 4}
	var waits [2]*syncWait
	var graphs [2]*update
	h.s.display.Do(func() {
		for i, surf := range []*surface{a, b} {
			fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
			if err != nil {
				t.Error(err)
				return
			}
			waits[i] = sw.add(fd)
			surf.next.sync = &commitSync{acquire: syncPoint{tl: tl, point: uint64(21 + i*2)}, release: syncPoint{tl: tl, point: uint64(22 + i*2)}, wait: waits[i]}
			surf.queueUpdate()
			graphs[i] = surf.queue[len(surf.queue)-1]
			graphs[i].refs++ // pin retired state for assertions
		}
		child.queueUpdate()
		child.queueUpdate()
		one, two := child.queue[0], child.queue[1]
		one.deps = []*update{graphs[0]}
		graphs[0].refs++
		two.deps = []*update{graphs[1]}
		graphs[1].refs++
		root.queueUpdate()
		u := root.queue[0]
		u.deps = []*update{two}
		two.refs++
		u.failed = true
		child.queueUpdate()
		later := child.queue[2]
		u.applyGraph()
		if len(child.queue) != 1 || child.queue[0] != later || len(a.queue) != 0 || len(b.queue) != 0 || tl.uses != 0 {
			t.Errorf("retirement stranded prefix graphs: uses=%d child=%d a=%d b=%d", tl.uses, len(child.queue), len(a.queue), len(b.queue))
		}
		for i, g := range graphs {
			if !g.retired || g.refs != 1 || !waits[i].cancelled.Load() {
				t.Error("grandchild not safely retired")
			}
			g.releaseRef()
		}
		child.dropQueue()
	})
	if len(h.signalled()) != 0 {
		t.Fatal("release signalled before acquire")
	}
}

func TestAcquireExportFailurePreservesCurrentContent(t *testing.T) {
	h := newSyncHarness(t)
	h.releases = map[uint32]*syncReleaseProxy{}
	if err := h.c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	h.s.display.Do(func() { h.dev.ExpectedCalls = nil })
	h.dev.EXPECT().destroy(mock.Anything).Return(nil).Maybe()
	h.dev.EXPECT().signalled(uint32(7), uint64(111)).Return(true, nil).Once()
	h.dev.EXPECT().exportSyncFile(uint32(7), uint64(111)).Return(nil, unix.EIO).Once()
	h.dev.EXPECT().signal(uint32(7), uint64(112)).Return(nil).Once() // observed acquire: safe discarded release
	drainContents(h.contents)
	buffer := h.dmabuf()
	var before ports.SurfaceContent
	h.s.display.Do(func() { before = h.s.windows[h.win].xdg.surface.content })
	h.commit(buffer, 111, 112)
	if got, ok := h.content(50 * time.Millisecond); ok {
		t.Fatalf("export failure published %+v", got)
	}
	h.s.display.Do(func() {
		surf := h.s.windows[h.win].xdg.surface
		if surf.content.Seq != before.Seq || surf.content.DMABuf != before.DMABuf || surf.hold.acquire != nil || len(surf.queue) != 0 {
			t.Error("export failure changed current content")
		}
		for _, tl := range h.s.timelines {
			if tl.uses != 0 {
				t.Error("export failure pinned timeline")
			}
		}
	})
}
