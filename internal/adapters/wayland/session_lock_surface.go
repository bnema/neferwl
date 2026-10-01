package wayland

import (
	"cmp"
	"slices"

	"github.com/bnema/go-wayland-bindings/server/extsessionlock"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
)

// lockConfigure is an issued configure, or the last acknowledged configure.
// It is copied into each commit: a later ACK cannot change a queued response.
type lockConfigure struct {
	serial        uint32
	width, height int
}

type lockSurface struct {
	resource       *extsessionlock.ExtSessionLockSurfaceV1
	server         *Server
	surface        *surface
	output         *output
	id             ports.WindowID
	serials        []lockConfigure
	ack            lockConfigure
	mapped, closed bool
	// onUnmap is a display-owner lifecycle notification, like Resource.OnDestroy.
	// The lock owner must remain protected; this never requests an unlock.
	onUnmap func(*lockSurface)
}

// newLockSurface only assigns the surface role. The caller validates ownership,
// admission, output lifetime and duplicate outputs before calling it. No global
// is registered and no locked/finished event is sent here.
func (s *Server) newLockSurface(r *extsessionlock.ExtSessionLockV1, id uint32, surf *surface, out *output, onUnmap func(*lockSurface)) (*lockSurface, error) {
	if surf.kind != roleNone {
		r.PostError(uint32(extsessionlock.ExtSessionLockV1ErrorRole), "surface already has a role")
		return nil, nil
	}
	if surf.current != nil || surf.next.buffer != nil || surf.bufferCommitted {
		r.PostError(uint32(extsessionlock.ExtSessionLockV1ErrorAlreadyConstructed), "surface has an attached or previously committed buffer")
		return nil, nil
	}
	l := &lockSurface{server: s, surface: surf, output: out, id: s.nextWindow, onUnmap: onUnmap}
	res, err := extsessionlock.NewExtSessionLockSurfaceV1(r.Client(), r.Version(), id, l)
	if err != nil {
		return nil, err
	}
	l.resource = res
	s.nextWindow++
	surf.kind, surf.lock, surf.role = roleSessionLock, l, l.commit
	if s.lockSurfaces == nil {
		s.lockSurfaces = make(map[ports.WindowID]*lockSurface)
	}
	s.lockSurfaces[l.id] = l
	s.lockSurfaceChanged()
	res.OnDestroy = l.unmap
	l.sendConfigure()
	return l, nil
}

// sendConfigure uses exact logical dimensions, not the physical output mode.
// Output inventory changes are routed here by the lock owner.
func (l *lockSurface) sendConfigure() {
	if l.closed || l.output == nil || !l.resource.Alive() {
		return
	}
	l.server.serial++
	// Serial zero is reserved internally as "no ACK".
	if l.server.serial == 0 {
		l.server.serial++
	}
	c := lockConfigure{serial: l.server.serial, width: l.output.place.Width, height: l.output.place.Height}
	l.serials = append(l.serials, c)
	l.resource.SendConfigure(c.serial, uint32(c.width), uint32(c.height))
}

func (l *lockSurface) AckConfigure(r *extsessionlock.ExtSessionLockSurfaceV1, serial uint32) {
	for i, c := range l.serials {
		if c.serial == serial {
			l.ack = c
			l.serials = l.serials[i+1:]
			return
		}
	}
	r.PostError(uint32(extsessionlock.ExtSessionLockSurfaceV1ErrorInvalidSerial), "unknown or consumed configure serial")
}

func (*lockSurface) Destroy(*extsessionlock.ExtSessionLockSurfaceV1) {}

// lockSurfaceChanged publishes validated placements for the admitted owner.
// Role commits never release protection or certify physical presentation.
func (s *Server) lockSurfaceChanged() {
	l := s.sessionLock
	if l == nil {
		return
	}
	placements := make([]ports.LockSurfacePlacement, 0, len(l.surfaces))
	// Before locked, only compositor black is displayed. Protocol configure
	// and buffer commits may still prepare the UI while proofs are pending.
	if l.locked {
		for _, surface := range l.surfaces {
			if surface.mapped && !surface.closed && surface.output != nil && surface.resource.Alive() {
				p := surface.output.place
				c := surface.surface.content
				if c.LogicalW == p.Width && c.LogicalH == p.Height {
					placements = append(placements, ports.LockSurfacePlacement{ID: surface.id, Output: surface.output.name(), Width: p.Width, Height: p.Height})
				}
			}
		}
	}
	slices.SortFunc(placements, func(a, b ports.LockSurfacePlacement) int { return cmp.Compare(a.ID, b.ID) })
	s.emit(ports.SessionLockChanged{State: l.state, Surfaces: placements})
}

// lockBuffer is the buffer after all earlier captured commits, not merely the
// currently applied buffer. A queued attach is sticky for subsequent commits.
func (s *surface) lockBuffer() *wayland.Buffer {
	if s.next.attached {
		return s.next.buffer
	}
	for i := len(s.queue) - 1; i >= 0; i-- {
		if s.queue[i].attached {
			return s.queue[i].buffer
		}
	}
	return s.current
}

// checkCommit runs before capture or fence acquisition. Waiting behind FIFO or
// an acquire fence must not let a later ACK legalize this commit.
func (l *lockSurface) checkCommit() bool {
	s := l.surface
	g := &surface{server: s.server, wl: s.wl, viewport: s.viewport, bufferScale: max(s.next.scale, 1), transform: s.next.transform}
	if v := s.viewport; v != nil {
		g.committedViewport = viewportState{destW: v.pendingW, destH: v.pendingH, dest: v.pendingSet, src: v.pendingSrc, crop: v.pendingCrop}
	}
	return l.checkGeometry(l.ack, s.lockBuffer(), g)
}

func (l *lockSurface) checkGeometry(ack lockConfigure, b *wayland.Buffer, g *surface) bool {
	if ack.serial == 0 {
		l.resource.PostError(uint32(extsessionlock.ExtSessionLockSurfaceV1ErrorCommitBeforeFirstAck), "commit before first configure ACK")
		return false
	}
	if b == nil {
		l.resource.PostError(uint32(extsessionlock.ExtSessionLockSurfaceV1ErrorNullBuffer), "null lock buffer")
		return false
	}
	state := l.server.buffers[b.Resource]
	var bw, bh int
	if state != nil {
		bw, bh = state.size()
	} else if sameBuffer(b, l.surface.current) && l.surface.has {
		// Destroying an applied wl_buffer does not detach its content. The
		// retained physical size remains authoritative for no-attach commits.
		bw, bh = l.surface.content.Width, l.surface.content.Height
	} else {
		l.resource.PostError(uint32(extsessionlock.ExtSessionLockSurfaceV1ErrorDimensionsMismatch), "unknown lock buffer dimensions")
		return false
	}
	if !g.validateViewport(bw, bh) {
		return false
	}
	w, h := g.logicalSize(bw, bh)
	if w != ack.width || h != ack.height {
		l.resource.PostError(uint32(extsessionlock.ExtSessionLockSurfaceV1ErrorDimensionsMismatch), "lock buffer does not match acknowledged logical dimensions")
		return false
	}
	return true
}

// checkCaptured validates only captured ACK and geometry, never live requests.
func (l *lockSurface) checkCaptured(u *update) bool {
	s := l.surface
	b := s.current
	if u.attached {
		b = u.buffer
	}
	g := &surface{server: s.server, wl: s.wl, viewport: u.vp, bufferScale: max(u.scale, 1), transform: u.transform}
	if u.vp != nil {
		g.committedViewport = viewportState{destW: u.vpW, destH: u.vpH, dest: u.vpSet, src: u.vpSrc, crop: u.vpCrop}
	}
	return l.checkGeometry(u.lockAck, b, g)
}

func (l *lockSurface) commit(buffer bool) {
	if l.surface.destroyed || !buffer {
		l.unmap()
		return
	}
	if !l.closed {
		l.mapped = true
		l.server.lockSurfaceChanged()
	}
}

// dropLockDeps retires the synchronized child prefixes captured by this root.
// Incoming graph links keep retired nodes readable until the root releases its
// refs. Later, uncaptured child commits remain owned by their own queues.
func (s *surface) dropLockDeps() {
	prefixes := make(map[*surface]int)
	var visit func(*update)
	seen := make(map[*update]bool)
	visit = func(u *update) {
		if u == nil || u.retired || seen[u] {
			return
		}
		seen[u] = true
		visit(u.prev)
		for _, dep := range u.deps {
			visit(dep)
			if dep.retired || dep.owner == s {
				continue
			}
			for i, queued := range dep.owner.queue {
				if queued == dep {
					prefixes[dep.owner] = max(prefixes[dep.owner], i+1)
					break
				}
			}
		}
	}
	for _, u := range s.queue {
		visit(u)
	}
	for child, n := range prefixes {
		child.dropQueuePrefix(n)
	}
}

// unmap also handles output loss/owner teardown. Role identity is permanent.
// Drop queued graphs through their normal retirement path and release current
// content while the mapped identity is still available to the hold machinery.
func (l *lockSurface) unmap() {
	if l.closed {
		return
	}
	s := l.surface
	// Send leaves while outgoing role identity remains resolvable. Destroying
	// the lock role may leave its wl_surface and seat resources alive.
	if l.server.seat.focused == l.id {
		l.server.changeFocus(0)
	}
	if l.server.seat.pointerFocus == l.id {
		l.server.changePointerFocus(0, 0, 0)
	}
	s.dropQueue()
	s.dropSync(s.next.sync)
	s.next.sync = nil
	if s.current != nil {
		l.server.releaseBuffer(s, s.current, s.hold)
	}
	s.current, s.next.buffer, s.hold = nil, nil, syncHold{}
	s.next.attached = false
	s.content, s.has = ports.SurfaceContent{}, false
	l.mapped, l.closed = false, true
	l.serials = nil
	delete(l.server.lockSurfaces, l.id)
	s.lock, s.role = nil, nil
	l.server.emitContent(ports.SurfaceContent{ID: l.id}, damage{full: true})
	l.server.lockSurfaceChanged()
	if l.onUnmap != nil {
		l.onUnmap(l)
	}
}
