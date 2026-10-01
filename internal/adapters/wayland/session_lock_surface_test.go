package wayland

import (
	"testing"
	"time"

	"github.com/bnema/purego-libwayland/protocol/extsessionlock"
	"github.com/bnema/purego-libwayland/protocol/linuxdrmsyncobj"
	"github.com/bnema/purego-libwayland/protocol/viewporter"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/protocol/wlrlayershell"
	"github.com/bnema/purego-libwayland/protocol/xdgshell"
	"github.com/bnema/wlturbo"
	"golang.org/x/sys/unix"
)

// This is a real wire proxy, not a double for a project interface.
type lockConfigureProxy struct {
	wlturbo.BaseProxy
	configures chan lockConfigure
}

func (p *lockConfigureProxy) Dispatch(e *wlturbo.Event) {
	if e.Opcode == uint16(extsessionlock.ExtSessionLockSurfaceV1EventConfigure) {
		p.configures <- lockConfigure{serial: e.Uint32(), width: int(e.Uint32()), height: int(e.Uint32())}
	}
}

func lockTestSurface(t *testing.T, s *Server, c *wlturbo.Display) (uint32, uint32, uint32, *surface, *lockSurface, <-chan lockConfigure) {
	t.Helper()
	comp := bindVersion(t, c, "wl_compositor", 6)
	surfID := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surfID)
	registerProtocol(t, c, surfID)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	ownerID, lockID := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, ownerID)
	setWireSchema(c, ownerID, "ext_session_lock_v1", 1)
	setWireSchema(c, lockID, "ext_session_lock_surface_v1", 1)
	configures := make(chan lockConfigure, 8)
	p := &lockConfigureProxy{configures: configures}
	p.SetID(lockID)
	registerWireProxy(c, p)
	var surf *surface
	var l *lockSurface
	s.display.Do(func() {
		for _, v := range s.surfaces {
			if v.wl.ID() == surfID {
				surf = v
			}
		}
		if surf == nil {
			t.Error("surface missing")
			return
		}
		// The role factory takes an already created owner resource. No manager
		// or test global is needed and no admission behavior is simulated.
		r, err := surf.wl.Client().CreateResource(extsessionlock.ExtSessionLockV1Interface, 1, ownerID, nil)
		if err != nil {
			t.Error(err)
			return
		}
		l, err = s.newLockSurface(extsessionlock.WrapExtSessionLockV1(r), lockID, surf, s.outputs[0], nil)
		if err != nil {
			t.Error(err)
		}
	})
	if l == nil {
		t.Fatal("lock role missing")
	}
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	return surfID, ownerID, lockID, surf, l, configures
}

func lockViewport(t *testing.T, c *wlturbo.Display, surfID uint32, w, h int) uint32 {
	t.Helper()
	manager := bindProtocol(t, c, "wp_viewporter")
	vp := c.AllocateID()
	registerProtocol(t, c, vp)
	requestProtocol(t, c, manager, viewporter.WpViewporterRequestGetViewport, vp, surfID)
	requestProtocol(t, c, vp, viewporter.WpViewportRequestSetDestination, int32(w), int32(h))
	return vp
}

func TestLockSurfaceInitialConfigureAndCommitErrors(t *testing.T) {
	for _, scenario := range []string{"before ack", "queued before ack", "null", "dimensions", "unknown ack", "reused ack", "old ack"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, _, dir := lifecycleServer(t)
			c := protocolClient(t, s, dir)
			surfID, _, lockID, surf, l, configures := lockTestSurface(t, s, c)
			cfg := <-configures
			if cfg.width != testOutputs[0].Width || cfg.height != testOutputs[0].Height || cfg.serial == 0 {
				t.Fatalf("configure = %+v", cfg)
			}
			code := extsessionlock.ExtSessionLockSurfaceV1ErrorInvalidSerial
			switch scenario {
			case "before ack", "queued before ack":
				if scenario == "queued before ack" {
					s.display.Do(func() { surf.barrier = true; surf.next.wait = true })
				}
				// Hold the display goroutine while both requests are written: the
				// server reads neither before the release, so the fatal commit
				// cannot close the connection under the ACK write.
				entered, release := make(chan struct{}), make(chan struct{})
				go s.display.Do(func() { close(entered); <-release })
				<-entered
				requestProtocol(t, c, surfID, wayland.SurfaceRequestCommit)
				// This later ACK must not legalize an earlier FIFO commit.
				requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestAckConfigure, cfg.serial)
				close(release)
				code = extsessionlock.ExtSessionLockSurfaceV1ErrorCommitBeforeFirstAck
			case "null":
				requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestAckConfigure, cfg.serial)
				requestProtocol(t, c, surfID, wayland.SurfaceRequestCommit)
				code = extsessionlock.ExtSessionLockSurfaceV1ErrorNullBuffer
			case "dimensions":
				requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestAckConfigure, cfg.serial)
				requestProtocol(t, c, surfID, wayland.SurfaceRequestAttach, shmBuffer(t, c), int32(0), int32(0))
				requestProtocol(t, c, surfID, wayland.SurfaceRequestCommit)
				code = extsessionlock.ExtSessionLockSurfaceV1ErrorDimensionsMismatch
			case "unknown ack":
				requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestAckConfigure, uint32(0))
			case "reused ack":
				requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestAckConfigure, cfg.serial)
				requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestAckConfigure, cfg.serial)
			case "old ack":
				s.display.Do(l.sendConfigure)
				if err := c.Roundtrip(); err != nil {
					t.Fatal(err)
				}
				newer := <-configures
				requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestAckConfigure, newer.serial)
				requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestAckConfigure, cfg.serial)
			}
			expectProtocolError(t, c, lockID, uint32(code))
		})
	}
}

func TestLockSurfaceCapturedAckGeometryAndDestroy(t *testing.T) {
	s, _, _, contents, dir := contentServer(t)
	c := protocolClient(t, s, dir)
	surfID, _, lockID, surf, l, configures := lockTestSurface(t, s, c)
	cfg := <-configures
	vpID := lockViewport(t, c, surfID, cfg.width, cfg.height)
	requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestAckConfigure, cfg.serial)
	requestProtocol(t, c, surfID, wayland.SurfaceRequestAttach, shmBuffer(t, c), int32(0), int32(0))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	// Capture a real buffer and geometry behind a future commit-time gate.
	s.display.Do(func() {
		surf.next.at = time.Now().Add(time.Hour)
		surf.Commit(surf.wl)
		if len(surf.queue) != 1 || surf.queue[0].lockAck != cfg {
			t.Errorf("captured ACK missing")
		}
		l.output.place.Width++
		l.sendConfigure()
	})
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	newer := <-configures
	requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestAckConfigure, newer.serial)
	requestProtocol(t, c, vpID, viewporter.WpViewportRequestSetDestination, int32(newer.width), int32(newer.height))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() {
		u := surf.queue[0]
		u.at = time.Time{}
		u.applyGraph()
		if !l.mapped || surf.committedViewport.destW != int32(cfg.width) || l.ack != newer {
			t.Errorf("captured commit read live geometry/ACK")
		}
		// Queue another update and destroy: retirement must clear all holds.
		surf.next.at = time.Now().Add(time.Hour)
		surf.Commit(surf.wl)
	})
	requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() {
		if surf.kind != roleSessionLock || surf.lock != nil || len(surf.queue) != 0 || surf.current != nil || len(s.lockSurfaces) != 0 || !l.closed {
			t.Error("destroy lost permanent role or retained queued content")
		}
	})
	select {
	case content := <-contents:
		if !content.Empty() {
			select {
			case content = <-contents:
				if !content.Empty() {
					t.Error("destroy did not emit empty content")
				}
			case <-time.After(time.Second):
				t.Fatal("no empty content")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("no content emitted")
	}
	wm := bindProtocol(t, c, "xdg_wm_base")
	requestProtocol(t, c, wm, xdgshell.WmBaseRequestGetXdgSurface, c.AllocateID(), surfID)
	expectProtocolError(t, c, wm, uint32(xdgshell.WmBaseErrorRole))
}

func TestLockSurfaceRejectsHistoricalBuffer(t *testing.T) {
	s, _, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	comp := bindProtocol(t, c, "wl_compositor")
	surfID := c.AllocateID()
	requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, surfID)
	registerProtocol(t, c, surfID)
	requestProtocol(t, c, surfID, wayland.SurfaceRequestAttach, shmBuffer(t, c), int32(0), int32(0))
	requestProtocol(t, c, surfID, wayland.SurfaceRequestCommit)
	requestProtocol(t, c, surfID, wayland.SurfaceRequestAttach, uint32(0), int32(0), int32(0))
	requestProtocol(t, c, surfID, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	ownerID, id := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, ownerID)
	s.display.Do(func() {
		for _, surf := range s.surfaces {
			if surf.wl.ID() != surfID {
				continue
			}
			r, err := surf.wl.Client().CreateResource(extsessionlock.ExtSessionLockV1Interface, 1, ownerID, nil)
			if err != nil {
				t.Error(err)
				return
			}
			if l, err := s.newLockSurface(extsessionlock.WrapExtSessionLockV1(r), id, surf, s.outputs[0], nil); l != nil || err != nil {
				t.Error("accepted historical buffer")
			}
		}
	})
	expectProtocolError(t, c, ownerID, uint32(extsessionlock.ExtSessionLockV1ErrorAlreadyConstructed))
}

func lockSizedBuffer(t *testing.T, c *wlturbo.Display, w, h int) uint32 {
	t.Helper()
	shm := bindProtocol(t, c, "wl_shm")
	registerProtocol(t, c, shm)
	fd, err := unix.MemfdCreate("lock-geometry", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	size := int32(w * h * 4)
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		t.Fatal(err)
	}
	pool, buffer := c.AllocateID(), c.AllocateID()
	registerProtocol(t, c, pool)
	registerProtocol(t, c, buffer)
	if err := wireRequest(c, shm, uint16(wayland.ShmRequestCreatePool), []int{fd}, pool, size); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, pool, wayland.ShmPoolRequestCreateBuffer, buffer, int32(0), int32(w), int32(h), int32(w*4), uint32(0))
	return buffer
}

func TestLockSurfaceScaleTransform(t *testing.T) {
	s, _, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	surfID, _, lockID, surf, l, configures := lockTestSurface(t, s, c)
	<-configures
	s.display.Do(func() { l.output.place.Width, l.output.place.Height = 3, 2; l.sendConfigure() })
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	cfg := <-configures
	requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestAckConfigure, cfg.serial)
	buffer := lockSizedBuffer(t, c, 4, 6)
	requestProtocol(t, c, surfID, wayland.SurfaceRequestSetBufferScale, int32(2))
	requestProtocol(t, c, surfID, wayland.SurfaceRequestSetBufferTransform, int32(1))
	requestProtocol(t, c, surfID, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
	requestProtocol(t, c, surfID, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() {
		if !l.mapped || surf.content.LogicalW != 3 || surf.content.LogicalH != 2 {
			t.Errorf("scaled transformed content = %+v", surf.content)
		}
	})
	requestProtocol(t, c, surfID, wayland.SurfaceRequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() {
		if !l.closed || len(s.lockSurfaces) != 0 {
			t.Error("wl_surface destruction did not unmap")
		}
	})
}

func TestLockSurfaceDestroyedReplacementAndViewportRetainsGeometry(t *testing.T) {
	s, _, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	surfID, _, lockID, surf, l, configures := lockTestSurface(t, s, c)
	cfg := <-configures
	vp := lockViewport(t, c, surfID, cfg.width, cfg.height)
	requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestAckConfigure, cfg.serial)
	first := shmBuffer(t, c)
	requestProtocol(t, c, surfID, wayland.SurfaceRequestAttach, first, int32(0), int32(0))
	requestProtocol(t, c, surfID, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() { l.output.place.Width++; l.sendConfigure() })
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	newer := <-configures
	requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestAckConfigure, newer.serial)
	requestProtocol(t, c, vp, viewporter.WpViewportRequestSetDestination, int32(newer.width), int32(newer.height))
	replacement := shmBuffer(t, c)
	requestProtocol(t, c, surfID, wayland.SurfaceRequestAttach, replacement, int32(0), int32(0))
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() { surf.next.at = time.Now().Add(time.Hour); surf.Commit(surf.wl) })
	requestProtocol(t, c, replacement, wayland.BufferRequestDestroy)
	requestProtocol(t, c, vp, viewporter.WpViewportRequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() {
		surf.queue[0].at = time.Time{}
		surf.queue[0].applyGraph()
		if !l.mapped || surf.current.ID() != first || surf.content.LogicalW != cfg.width || surf.committedViewport.destW != int32(cfg.width) {
			t.Error("destroyed replacement changed retained geometry")
		}
	})
}

func TestLockSurfaceDestroyedCurrentBufferAllowsNoAttachCommit(t *testing.T) {
	s, _, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	surfID, _, lockID, surf, l, configures := lockTestSurface(t, s, c)
	cfg := <-configures
	lockViewport(t, c, surfID, cfg.width, cfg.height)
	requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestAckConfigure, cfg.serial)
	buffer := shmBuffer(t, c)
	requestProtocol(t, c, surfID, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
	requestProtocol(t, c, surfID, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	requestProtocol(t, c, buffer, wayland.BufferRequestDestroy)
	callback := c.AllocateID()
	registerProtocol(t, c, callback)
	requestProtocol(t, c, surfID, wayland.SurfaceRequestFrame, callback)
	requestProtocol(t, c, surfID, wayland.SurfaceRequestCommit)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	s.display.Do(func() {
		if !l.mapped || !surf.has || surf.content.LogicalW != cfg.width {
			t.Error("retained destroyed buffer unmapped")
		}
	})
}

func TestLockSurfaceDestroyBlocksLayerReuse(t *testing.T) {
	s, _, _, dir := lifecycleServer(t)
	c := protocolClient(t, s, dir)
	surfID, _, lockID, _, _, _ := lockTestSurface(t, s, c)
	requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestDestroy)
	if err := c.Roundtrip(); err != nil {
		t.Fatal(err)
	}
	shell := bindVersion(t, c, "zwlr_layer_shell_v1", 4)
	requestProtocol(t, c, shell, wlrlayershell.ZwlrLayerShellV1RequestGetLayerSurface, c.AllocateID(), surfID, uint32(0), uint32(2), "role-reuse")
	expectProtocolError(t, c, shell, uint32(wlrlayershell.ZwlrLayerShellV1ErrorAlreadyConstructed))
}

func TestLockSurfaceDestroyRetiresFencedChildDependencies(t *testing.T) {
	for _, destroy := range []string{"role", "surface", "role ready"} {
		t.Run(destroy, func(t *testing.T) {
			h := newSyncHarness(t)
			h.releases = map[uint32]*syncReleaseProxy{}
			c, s := h.c, h.s
			rootID, _, lockID, root, _, configs := lockTestSurface(t, s, c)
			cfg := <-configs
			lockViewport(t, c, rootID, cfg.width, cfg.height)
			requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestAckConfigure, cfg.serial)
			rootBuffer := shmBuffer(t, c)
			requestProtocol(t, c, rootID, wayland.SurfaceRequestAttach, rootBuffer, int32(0), int32(0))
			requestProtocol(t, c, rootID, wayland.SurfaceRequestCommit)
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			comp := bindProtocol(t, c, "wl_compositor")
			subcomp := bindProtocol(t, c, "wl_subcompositor")
			mgr := bindProtocol(t, c, "wp_linux_drm_syncobj_manager_v1")
			childID := c.AllocateID()
			registerProtocol(t, c, childID)
			requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, childID)
			sub := c.AllocateID()
			registerProtocol(t, c, sub)
			requestProtocol(t, c, subcomp, wayland.SubcompositorRequestGetSubsurface, sub, childID, rootID)
			syncID := c.AllocateID()
			registerProtocol(t, c, syncID)
			requestProtocol(t, c, mgr, linuxdrmsyncobj.WpLinuxDrmSyncobjManagerV1RequestGetSurface, syncID, childID)
			h.surf, h.syncSurf = childID, syncID
			buffer := h.dmabuf()
			h.commit(buffer, 31, 32)
			var child *surface
			var dep, parent *update
			var wait *syncWait
			var tl *timeline
			s.display.Do(func() {
				for _, v := range s.surfaces {
					if v.wl.ID() == childID {
						child = v
					}
				}
				root.next.at = time.Now().Add(time.Hour)
				root.Commit(root.wl)
				dep, parent = child.queue[0], root.queue[0]
				dep.refs++ // Pin for inspection just like another graph consumer.
				parent.refs++
				wait, tl = dep.sync.wait, dep.sync.acquire.tl
				if !dep.synced || !dep.bound || len(parent.deps) != 1 || tl.uses != 2 {
					t.Error("child was not captured with its fence")
				}
			})
			if destroy == "role ready" {
				h.fire(31)
				deadline := time.NewTimer(time.Second)
				defer deadline.Stop()
				ready := false
				for !ready {
					s.display.Do(func() { ready = child.syncReady(dep.sync) })
					if ready {
						break
					}
					select {
					case <-deadline.C:
						t.Fatal("acquire waiter did not become ready")
					case <-time.After(time.Millisecond):
					}
				}
			}
			// A later child commit is not owned by the captured root graph.
			requestProtocol(t, c, childID, wayland.SurfaceRequestCommit)
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			if destroy != "surface" {
				requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestDestroy)
			} else {
				requestProtocol(t, c, rootID, wayland.SurfaceRequestDestroy)
			}
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			s.display.Do(func() {
				remaining, refs := 1, 2 // later child commit retains its prev link
				if destroy == "surface" {
					remaining, refs = 0, 1 // parent destruction detaches the child
				}
				if len(root.queue) != 0 || len(child.queue) != remaining || !dep.retired || !parent.retired || dep.refs != refs || parent.refs != 1 || !wait.cancelled.Load() || tl.uses != 0 {
					t.Errorf("dependency teardown: child=%d dep refs=%d parent refs=%d uses=%d cancelled=%v", len(child.queue), dep.refs, parent.refs, tl.uses, wait.cancelled.Load())
				}
				dep.applyGraph()
				parent.applyGraph()
				if child.current != nil || root.current != nil {
					t.Error("retired graph replayed")
				}
				child.dropQueue()
				if dep.refs != 1 {
					t.Errorf("retired dependency refs after later commit retirement = %d", dep.refs)
				}
				dep.releaseRef()
				parent.releaseRef()
			})
			count := 0
			for _, point := range h.signalled() {
				if point == 32 {
					count++
				}
			}
			want := 0
			if destroy == "role ready" {
				want = 1
			}
			if count != want {
				t.Errorf("release signals = %d, want %d", count, want)
			}
			select {
			case <-h.releases[buffer].released:
				t.Error("explicit-sync buffer received wl_buffer.release")
			default:
			}
		})
	}
}

func TestLockSurfaceChildPrefixKeepsSharedBufferOwnership(t *testing.T) {
	for _, preserved := range []string{"suffix", "pending", "implicit suffix"} {
		t.Run(preserved, func(t *testing.T) {
			var s *Server
			var c *wlturbo.Display
			var h *syncHarness
			if preserved == "implicit suffix" {
				h = newSyncHarness(t)
				h.releases = map[uint32]*syncReleaseProxy{}
				s, c = h.s, h.c
			} else {
				var dir string
				s, _, _, dir = lifecycleServer(t)
				c = protocolClient(t, s, dir)
			}
			rootID, _, lockID, root, _, configs := lockTestSurface(t, s, c)
			cfg := <-configs
			lockViewport(t, c, rootID, cfg.width, cfg.height)
			requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestAckConfigure, cfg.serial)
			requestProtocol(t, c, rootID, wayland.SurfaceRequestAttach, shmBuffer(t, c), int32(0), int32(0))
			requestProtocol(t, c, rootID, wayland.SurfaceRequestCommit)
			comp := bindProtocol(t, c, "wl_compositor")
			subcomp := bindProtocol(t, c, "wl_subcompositor")
			childID := c.AllocateID()
			registerProtocol(t, c, childID)
			requestProtocol(t, c, comp, wayland.CompositorRequestCreateSurface, childID)
			sub := c.AllocateID()
			registerProtocol(t, c, sub)
			requestProtocol(t, c, subcomp, wayland.SubcompositorRequestGetSubsurface, sub, childID, rootID)
			var buffer uint32
			if h != nil {
				buffer = h.dmabuf()
			} else {
				buffer = shmBuffer(t, c)
			}
			releases := &syncReleaseProxy{released: make(chan struct{}, 4)}
			releases.SetID(buffer)
			registerWireProxy(c, releases)
			requestProtocol(t, c, childID, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
			requestProtocol(t, c, childID, wayland.SurfaceRequestCommit)
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			s.display.Do(func() { root.next.at = time.Now().Add(time.Hour); root.Commit(root.wl) })
			requestProtocol(t, c, childID, wayland.SurfaceRequestAttach, buffer, int32(0), int32(0))
			if preserved != "pending" {
				requestProtocol(t, c, childID, wayland.SurfaceRequestCommit)
			}
			requestProtocol(t, c, lockID, extsessionlock.ExtSessionLockSurfaceV1RequestDestroy)
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-releases.released:
				t.Fatal("captured prefix released shared buffer early")
			default:
			}
			// Convert the pending reference to an update, then retire the last
			// reference through the same normal queue path.
			if preserved == "pending" {
				requestProtocol(t, c, childID, wayland.SurfaceRequestCommit)
				if err := c.Roundtrip(); err != nil {
					t.Fatal(err)
				}
			}
			s.display.Do(func() {
				child := s.surfaceOf(findTestSurface(s, childID))
				if len(child.queue) != 1 {
					t.Errorf("preserved queue length = %d", len(child.queue))
				}
				child.dropQueue()
			})
			if err := c.Roundtrip(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-releases.released:
			default:
				t.Fatal("last queued reference did not release buffer")
			}
			select {
			case <-releases.released:
				t.Fatal("buffer released twice")
			default:
			}
		})
	}
}

func findTestSurface(s *Server, id uint32) *wayland.Surface {
	for _, surf := range s.surfaces {
		if surf.wl.ID() == id {
			return surf.wl
		}
	}
	return nil
}
