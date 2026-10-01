package wayland

import (
	"context"

	"github.com/bnema/go-wayland-bindings/server/extsessionlock"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
)

// sessionLock is owned by the display goroutine. Resource destruction removes
// its owner, never the protection gate. An ordinary new owner may take over.
type sessionLock struct {
	s        *Server
	res      *extsessionlock.ExtSessionLockV1
	state    ports.SecurityState
	locked   bool
	surfaces map[*output]*lockSurface
}

type sessionLockManager struct{ s *Server }

// registerSessionLock is enabled only when the display and backend share
// protection control and authoritative, non-dropping proof channels.
func registerSessionLock(d *server.Display, s *Server) error {
	return extsessionlock.NewExtSessionLockManagerV1Global(d, 1, func(c server.Client, version, id uint32) {
		_, _ = extsessionlock.NewExtSessionLockManagerV1(c, int32(version), id, sessionLockManager{s})
	})
}
func (sessionLockManager) Destroy(*extsessionlock.ExtSessionLockManagerV1) {}

func (m sessionLockManager) Lock(r *extsessionlock.ExtSessionLockManagerV1, id uint32) {
	s := m.s
	l := &sessionLock{s: s, surfaces: make(map[*output]*lockSurface)}
	res, err := extsessionlock.NewExtSessionLockV1(r.Client(), r.Version(), id, l)
	if err != nil {
		return
	}
	l.res = res
	if s.securityController == nil || s.channels.SecurityChanges == nil || s.channels.SecurityEvents == nil || s.sessionLock != nil || len(s.securityPending) >= maxSecurityPending {
		res.SendFinished()
		return
	}
	// Engage before queueing any core event. No display-owned protocol
	// admission can observe the old policy after this point.
	state, err := s.securityController.Engage()
	if err != nil {
		res.SendFinished()
		return
	}
	l.state = state
	s.sessionLock = l
	s.lockReadiness.Begin(state.Generation)
	res.OnDestroy = l.orphan
	s.resetProtectionInput()
	s.endCaptureSessions()
	for _, lease := range s.activeLeases {
		lease.destroy()
	}
	// Preserve card tombstones until late replies are consumed/revoked, but
	// revoke their protocol authority across both acquire and release.
	for id, lease := range s.pendingLeases {
		lease.finish()
		delete(s.pendingLeases, id)
	}
	s.updateWorkspaceManagers(s.workspaceSnapshot)
	s.emit(ports.SessionLockChanged{State: state})
	s.requestBackendSecurity(state)
	s.maybeLocked()
}

func (l *sessionLock) Destroy(r *extsessionlock.ExtSessionLockV1) {
	if l.locked {
		r.PostError(uint32(extsessionlock.ExtSessionLockV1ErrorInvalidDestroy), "destroy while locked")
	}
}
func (l *sessionLock) UnlockAndDestroy(r *extsessionlock.ExtSessionLockV1) {
	s := l.s
	if !l.locked || s.sessionLock != l {
		r.PostError(uint32(extsessionlock.ExtSessionLockV1ErrorInvalidUnlock), "unlock before protection confirmation")
		return
	}
	state, err := s.securityController.Release(l.state.Generation)
	if err != nil {
		r.PostError(uint32(extsessionlock.ExtSessionLockV1ErrorInvalidUnlock), "protection release rejected")
		return
	}
	// Clear input before any desktop focus enter; no password held keys.
	s.resetProtectionInput()
	s.lockReadiness.End(l.state.Generation)
	s.sessionLock = nil
	s.refreshToplevels()
	s.updateWorkspaceManagers(s.workspaceSnapshot)
	for _, surface := range l.surfaces {
		surface.unmap()
	}
	s.emit(ports.SessionLockChanged{State: state})
	s.requestBackendSecurity(state)
}

func (l *sessionLock) GetLockSurface(r *extsessionlock.ExtSessionLockV1, id uint32, wl *wayland.Surface, out *wayland.Output) {
	s := l.s
	if s.sessionLock != l {
		_, _ = extsessionlock.NewExtSessionLockSurfaceV1(r.Client(), r.Version(), id, refusedLockSurface{})
		return
	}
	surface, output := s.surfaceOf(wl), s.outputOf(out)
	if surface == nil || output == nil || wl == nil || out == nil || wl.Client() != r.Client() || out.Client() != r.Client() {
		r.PostError(uint32(extsessionlock.ExtSessionLockV1ErrorRole), "lock surface and output must belong to lock owner")
		return
	}
	if _, exists := l.surfaces[output]; output != nil && exists {
		r.PostError(uint32(extsessionlock.ExtSessionLockV1ErrorDuplicateOutput), "duplicate lock output")
		return
	}
	ls, err := s.newLockSurface(r, id, surface, output, func(*lockSurface) { s.lockSurfaceChanged() })
	if err != nil || ls == nil {
		return
	}
	l.surfaces[output] = ls
	s.lockSurfaceChanged()
}

func (l *sessionLock) orphan() {
	s := l.s
	if s.sessionLock != l {
		return
	}
	// Never release or send finished merely because a client died.
	s.resetProtectionInput()
	s.sessionLock = nil
	for _, surface := range l.surfaces {
		surface.unmap()
	}
	s.emit(ports.SessionLockChanged{State: l.state})
}

func (s *Server) resetProtectionInput() {
	s.changeFocus(0)
	s.changePointerFocus(0, 0, 0)
	s.deactivateText()
	s.deactivateConstraint()
	clear(s.seat.heldKeys)
	clear(s.seat.grabKeys)
	s.seat.modState = ports.ModState{}
	s.seat.press, s.seat.pressClient = 0, server.Client{}
	s.seat.wheelRest, s.seat.wheelHeld = [2]int32{}, [2]float64{}
	s.cursorSurface = nil
}

// maxSecurityPending bounds takeover churn while the backend is stalled.
// Acquire checks this bound before engaging; release retains a reserved slot.
const maxSecurityPending = 16

func (s *Server) requestBackendSecurity(state ports.SecurityState) {
	s.securityPending = append(s.securityPending, state)
	select {
	case s.securityWake <- struct{}{}:
	default:
	}
}

// forwardBackendSecurity transfers immutable states without blocking display
// dispatch. Queue mutation is always on the display owner, not this goroutine.
func (s *Server) forwardBackendSecurity(ctx context.Context) {
	for {
		var state ports.SecurityState
		var pending bool
		if !s.display.Do(func() {
			if len(s.securityPending) != 0 {
				state, pending = s.securityPending[0], true
			}
		}) {
			return
		}
		if !pending {
			select {
			case <-ctx.Done():
				return
			case <-s.display.Stopped():
				return
			case <-s.securityWake:
				continue
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-s.display.Stopped():
			return
		case s.channels.SecurityChanges <- state:
		}
		if !s.display.Do(func() {
			// Only this sender pops; appending on display preserves the head.
			if len(s.securityPending) != 0 && s.securityPending[0] == state {
				s.securityPending[0] = ports.SecurityState{}
				s.securityPending = s.securityPending[1:]
			}
		}) {
			return
		}
	}
}

func (s *Server) forwardSecurityEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.display.Stopped():
			return
		case event, ok := <-s.channels.SecurityEvents:
			if !ok {
				return
			}
			if !s.display.Do(func() { s.applySecurityEvent(event) }) {
				return
			}
		}
	}
}

func (s *Server) applySecurityEvent(event ports.SecurityBackendEvent) {
	switch v := event.(type) {
	case ports.SecurityOutputAdded:
		s.lockReadiness.Add(v.Instance)
	case ports.SecurityOutputRemoved:
		s.lockReadiness.Remove(v.Instance)
	case ports.SecurityOutputProof:
		s.lockReadiness.Record(v.Proof)
	case ports.SecurityOutputInvalidated:
		s.lockReadiness.Invalidate(v.Generation, v.Instance)
	case ports.SecurityBackendBarrier:
		if v.Err == nil {
			s.lockReadiness.BackendBarrier(v.Generation)
		} else {
			s.log.Error().Err(v.Err).Msg("session security backend barrier failed")
		}
	}
	s.maybeLocked()
}

func (s *Server) lockInputTarget(id ports.WindowID) bool {
	l := s.sessionLock
	if l == nil || !l.locked {
		return false
	}
	surface := s.lockSurfaces[id]
	if surface == nil || !surface.mapped || surface.closed || !surface.resource.Alive() {
		return false
	}
	for _, owned := range l.surfaces {
		if surface == owned {
			return true
		}
	}
	return false
}

func (s *Server) maybeLocked() {
	l := s.sessionLock
	if l == nil || l.locked || !l.res.Alive() {
		return
	}
	if len(s.captureInflight) == 0 {
		s.lockReadiness.CaptureBarrier(l.state.Generation)
	}
	if !s.lockReadiness.Ready() {
		return
	}
	l.locked = true
	l.res.SendLocked()
	s.lockSurfaceChanged()
}
