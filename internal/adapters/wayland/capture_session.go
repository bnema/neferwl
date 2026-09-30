package wayland

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"os"
	"slices"
	"time"

	"github.com/bnema/neferwl/internal/adapters/wayland/capturesession"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/server"
)

// Private capture sessions (neferwl_capture_manager_v1, capturesession/neferwl-capture-v1.xml).
// Core owns the session state; this file relays requests, tells clients what
// core decided and tags the capture requests of the session's own connection.
//
// Privacy: the session token never reaches a log, and neither does any peer
// identity; only session lifecycle states are logged.

// privateCapture is the server's private capture state; display goroutine only.
type privateCapture struct {
	ownUID   uint32
	managers []*captureManager
	next     uint64
	session  *privateSession // the one live session
	// dead are ended sessions whose owner has not destroyed the object yet.
	dead []*privateSession
}

// minPingGap drops pings that follow the previous one too closely: the
// timeout is seconds, so an owner cannot flood core with them.
const minPingGap = ports.CaptureSessionTimeout / 50

type privateSession struct {
	s      *Server
	id     uint64
	token  string
	res    *capturesession.NeferwlCaptureSessionV1
	client server.Client // the owner's connection: its captures are clean
	output string
	// layers are the attached layer surfaces; attached their attachment
	// objects, pending until a core state lists the layer.
	layers   []*layerSurface
	attached []*layerAttachment
	lastPing time.Time
	// confirmed is set by the first core state; active and revision are the
	// last state's: clean captures are tagged with them.
	confirmed, active bool
	revision          uint64
}

// layerAttachment is one neferwl_capture_layer_v1: it reports the result of
// an attach_surface and the layer's end.
type layerAttachment struct {
	res       *capturesession.NeferwlCaptureLayerV1
	layer     *layerSurface
	confirmed bool // attached sent
}

func (a *layerAttachment) fail(reason capturesession.NeferwlCaptureLayerV1Failure) {
	if a.res.Alive() {
		a.res.SendFailed(uint32(reason))
	}
}

// finish ends an attachment: detached after attached, failed before it.
func (a *layerAttachment) finish(failure capturesession.NeferwlCaptureLayerV1Failure, detach capturesession.NeferwlCaptureLayerV1DetachReason) {
	if !a.res.Alive() {
		return
	}
	if a.confirmed {
		a.res.SendDetached(uint32(detach))
	} else {
		a.res.SendFailed(uint32(failure))
	}
}

// trustedPeer reports whether a peer with these credentials may use the
// private protocol: its credentials are readable and it runs as the
// compositor's own user.
func trustedPeer(own uint32, peer server.Credentials, err error) bool {
	return err == nil && peer.UID == own
}

func (s *Server) trusted(c server.Client) bool {
	cr, err := c.Credentials()
	return trustedPeer(s.private.ownUID, cr, err)
}

func registerCaptureSession(d *server.Display, s *Server) error {
	s.private.ownUID = uint32(os.Geteuid())
	return capturesession.NewNeferwlCaptureManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		m := &captureManager{s: s, trusted: s.trusted(c), known: map[uint64]privateWorkspace{}}
		r, err := capturesession.NewNeferwlCaptureManagerV1(c, int32(v), id, m)
		if err != nil {
			return
		}
		m.res = r
		s.private.managers = append(s.private.managers, m)
		r.OnDestroy = func() { s.private.managers = removeItem(s.private.managers, m) }
		m.update(s.workspaceSnapshot)
	})
}

type privateWorkspace struct {
	output string
	info   ports.WorkspaceInfo
}

type captureManager struct {
	s       *Server
	res     *capturesession.NeferwlCaptureManagerV1
	trusted bool
	known   map[uint64]privateWorkspace
}

// update sends the workspaces that are new or changed, and the ones gone.
// Untrusted peers get no inventory.
func (m *captureManager) update(snapshot ports.Workspaces) {
	if m.s.protected() || !m.trusted || !m.res.Alive() {
		return
	}
	now := map[uint64]privateWorkspace{}
	for _, out := range snapshot.Outputs {
		for _, w := range out.Workspaces {
			now[w.ID] = privateWorkspace{out.Name, w}
		}
	}
	for id := range m.known {
		if _, ok := now[id]; !ok {
			m.res.SendWorkspaceRemoved(uint32(id>>32), uint32(id))
		}
	}
	for id, w := range now {
		if m.known[id] == w {
			continue
		}
		f, active := w.info.Frame, uint32(0)
		if w.info.Active {
			active = 1
		}
		m.res.SendWorkspace(uint32(id>>32), uint32(id), w.output, w.info.Name, int32(f.X), int32(f.Y), int32(f.W), int32(f.H), active)
	}
	m.known = now
}

// updateCaptureWorkspaces relays a core workspace snapshot to the private managers.
func (s *Server) updateCaptureWorkspaces(snapshot ports.Workspaces) {
	for _, m := range s.private.managers {
		m.update(snapshot)
	}
}

func (*captureManager) Destroy(*capturesession.NeferwlCaptureManagerV1) {}

func (m *captureManager) BeginSession(r *capturesession.NeferwlCaptureManagerV1, id uint32, out *wayland.Output, x, y, w, h int32, hi, lo, record uint32) {
	s := m.s
	if out == nil {
		r.PostError(uint32(capturesession.NeferwlCaptureManagerV1ErrorInvalidOutput), "no output")
		return
	}
	if w < 0 || h < 0 {
		r.PostError(uint32(capturesession.NeferwlCaptureManagerV1ErrorInvalidRegion), "negative region size")
		return
	}
	ps := &privateSession{s: s, client: r.Client()}
	res, err := capturesession.NewNeferwlCaptureSessionV1(r.Client(), r.Version(), id, ps)
	if err != nil {
		return
	}
	ps.res = res
	stop := func(reason capturesession.NeferwlCaptureSessionV1StopReason) { res.SendStopped(uint32(reason)) }
	o := s.outputOf(out)
	switch {
	case s.protected() || !m.trusted:
		s.log.Info().Str("reason", string(ports.CaptureReasonUnauthorized)).Msg("capture session refused")
		stop(capturesession.NeferwlCaptureSessionV1StopReasonUnauthorized)
		return
	case s.private.session != nil:
		s.log.Info().Str("reason", string(ports.CaptureReasonBusy)).Msg("capture session refused")
		stop(capturesession.NeferwlCaptureSessionV1StopReasonBusy)
		return
	case o == nil:
		stop(capturesession.NeferwlCaptureSessionV1StopReasonOutputGone)
		return
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		s.log.Warn().Msg("capture session refused: no randomness")
		stop(capturesession.NeferwlCaptureSessionV1StopReasonUnauthorized)
		return
	}
	s.private.next++
	ps.id, ps.token, ps.output = s.private.next, hex.EncodeToString(raw[:]), o.name()
	s.private.session = ps
	res.OnDestroy = ps.release
	workspace := uint64(hi)<<32 | uint64(lo)
	s.log.Info().Uint64("session", ps.id).Str("output", ps.output).Uint64("workspace", workspace).Bool("record", record != 0).Msg("capture session begin")
	s.emit(ports.CaptureSessionBegin{ID: ps.id, Output: ps.output, Workspace: workspace, Region: ports.Rect{X: int(x), Y: int(y), W: int(w), H: int(h)}, Record: record != 0})
}

func (m *captureManager) AttachSurface(r *capturesession.NeferwlCaptureManagerV1, id uint32, token string, surface *wayland.Surface) {
	s := m.s
	if surface == nil || s.surfaces[surface.Resource] == nil || surface.Resource.Client() != r.Client() {
		r.PostError(uint32(capturesession.NeferwlCaptureManagerV1ErrorInvalidSurface), "surface is not yours")
		return
	}
	state := s.surfaces[surface.Resource]
	l := state.layer
	att := &layerAttachment{layer: l}
	res, err := capturesession.NewNeferwlCaptureLayerV1(r.Client(), r.Version(), id, att)
	if err != nil {
		return
	}
	att.res = res
	// Same user as the compositor, and as the session's creator (who passed
	// the same check): nothing else can attach.
	ps := s.private.session
	switch {
	case s.protected() || !m.trusted: // DISPLAY protection and bind-time UID
		att.fail(capturesession.NeferwlCaptureLayerV1FailureUnauthorized)
		return
	case ps == nil || subtle.ConstantTimeCompare([]byte(token), []byte(ps.token)) != 1:
		att.fail(capturesession.NeferwlCaptureLayerV1FailureUnknownToken)
		return
	case state.kind != roleLayer || l == nil:
		r.PostError(uint32(capturesession.NeferwlCaptureManagerV1ErrorSurfaceRole), "not a layer surface")
		return
	case l.pending.layer != ports.LayerTop && l.pending.layer != ports.LayerOverlay:
		r.PostError(uint32(capturesession.NeferwlCaptureManagerV1ErrorInvalidLayer), "layer must be top or overlay")
		return
	case l.pending.keyboard == 1:
		r.PostError(uint32(capturesession.NeferwlCaptureManagerV1ErrorExclusiveKeyboard), "exclusive keyboard")
		return
	case l.mapped || l.closed:
		r.PostError(uint32(capturesession.NeferwlCaptureManagerV1ErrorInvalidSurface), "already mapped")
		return
	case l.capture != nil:
		att.fail(capturesession.NeferwlCaptureLayerV1FailureAlreadyAttached)
		return
	case len(ps.layers) >= ports.MaxCaptureSessionLayers:
		att.fail(capturesession.NeferwlCaptureLayerV1FailureTooManyLayers)
		return
	}
	l.capture, l.attachment = ps, att
	l.pending.keyboard, l.current.keyboard = 0, 0
	ps.layers = append(ps.layers, l)
	ps.attached = append(ps.attached, att)
	// Queued before any LayerChanged of this layer: core excludes it from
	// the first scene that shows it.
	s.emit(ports.CaptureSessionLayer{ID: ps.id, Layer: l.id, Attached: true})
}

func (*layerAttachment) Destroy(*capturesession.NeferwlCaptureLayerV1) {}

func (ps *privateSession) Destroy(*capturesession.NeferwlCaptureSessionV1) {}

func (ps *privateSession) Ping(*capturesession.NeferwlCaptureSessionV1) {
	if ps.s.private.session != ps {
		return
	}
	if now := time.Now(); now.Sub(ps.lastPing) >= minPingGap {
		ps.lastPing = now
		ps.s.emit(ports.CaptureSessionPing{ID: ps.id})
	}
}

// release runs when the session object goes: destroy request or client
// disconnect. A live session ends and core is told to forget it.
func (ps *privateSession) release() {
	s := ps.s
	if s.private.session == ps {
		s.log.Info().Uint64("session", ps.id).Str("reason", "owner-gone").Msg("capture session end")
		ps.end(true)
	}
	s.private.dead = removeItem(s.private.dead, ps)
}

// end ends the session: every attachment is told (detached, or failed before
// it was confirmed), its layer surfaces are closed so none lingers into the
// next session, and the owner's captures of the output stay refused until it
// destroys the session object. notify tells core.
func (ps *privateSession) end(notify bool) {
	s := ps.s
	if s.private.session != ps {
		return
	}
	s.private.session = nil
	if ps.res.Alive() {
		s.private.dead = append(s.private.dead, ps)
	}
	layers, attached := ps.layers, ps.attached
	ps.layers, ps.attached = nil, nil
	for _, a := range attached {
		a.finish(capturesession.NeferwlCaptureLayerV1FailureSessionEnded, capturesession.NeferwlCaptureLayerV1DetachReasonSessionEnded)
	}
	for _, l := range layers {
		l.capture, l.attachment = nil, nil
		// Closing unmaps the surface: the LayerChanged that removes it is
		// queued before the CaptureSessionEnd below.
		l.close()
	}
	if notify {
		s.emit(ports.CaptureSessionEnd{ID: ps.id})
	}
}

// captureDetach drops a layer surface whose role is gone from its session.
// The caller unmapped it first, so the LayerChanged that removes the layer is
// already queued: core keeps excluding the layer until it reads that.
func (l *layerSurface) captureDetach() {
	ps, att := l.capture, l.attachment
	if ps == nil {
		return
	}
	l.capture, l.attachment = nil, nil
	ps.layers = removeItem(ps.layers, l)
	ps.attached = removeItem(ps.attached, att)
	att.finish(capturesession.NeferwlCaptureLayerV1FailureLayerDestroyed, capturesession.NeferwlCaptureLayerV1DetachReasonLayerDestroyed)
	if ps.s.private.session == ps {
		ps.s.emit(ports.CaptureSessionLayer{ID: ps.id, Layer: l.id})
	}
}

// stopReason maps a terminal core reason to the wire.
func stopReason(r ports.CaptureReason) capturesession.NeferwlCaptureSessionV1StopReason {
	switch r {
	case ports.CaptureReasonOutputGone:
		return capturesession.NeferwlCaptureSessionV1StopReasonOutputGone
	case ports.CaptureReasonOutputOff:
		return capturesession.NeferwlCaptureSessionV1StopReasonOutputOff
	case ports.CaptureReasonPingTimeout:
		return capturesession.NeferwlCaptureSessionV1StopReasonPingTimeout
	case ports.CaptureReasonWorkspaceGone:
		return capturesession.NeferwlCaptureSessionV1StopReasonWorkspaceGone
	case ports.CaptureReasonBusy:
		return capturesession.NeferwlCaptureSessionV1StopReasonBusy
	case ports.CaptureReasonInvalidRegion:
		return capturesession.NeferwlCaptureSessionV1StopReasonInvalidRegion
	case ports.CaptureReasonUnauthorized:
		return capturesession.NeferwlCaptureSessionV1StopReasonUnauthorized
	case ports.CaptureReasonTooManyExcluded:
		return capturesession.NeferwlCaptureSessionV1StopReasonTooManyExcluded
	}
	return capturesession.NeferwlCaptureSessionV1StopReasonRequested
}

// captureState applies what core decided about the session.
func (s *Server) captureState(c ports.CaptureSessionState) {
	ps := s.private.session
	if ps == nil || ps.id != c.ID {
		return
	}
	if c.Reason.Terminal() {
		s.log.Info().Uint64("session", ps.id).Str("reason", string(c.Reason)).Msg("capture session end")
		if ps.res.Alive() {
			ps.res.SendStopped(uint32(stopReason(c.Reason)))
		}
		ps.end(false)
		return
	}
	ps.confirmed, ps.active, ps.revision = true, c.Active, c.Revision
	s.log.Debug().Uint64("session", ps.id).Bool("active", c.Active).Uint64("revision", c.Revision).Str("reason", string(c.Reason)).Msg("capture session state")
	if ps.res.Alive() {
		active := uint32(0)
		if c.Active {
			active = 1
		}
		ps.res.SendState(ps.token, active, int32(c.Rect.X), int32(c.Rect.Y), int32(c.Rect.W), int32(c.Rect.H), uint32(c.Workspace>>32), uint32(c.Workspace), uint32(c.Revision>>32), uint32(c.Revision))
	}
	// A layer is confirmed once core lists it: only then is it safe for the
	// client to show it.
	for _, a := range ps.attached {
		if !a.confirmed && slices.Contains(c.Layers, a.layer.id) {
			a.confirmed = true
			if a.res.Alive() {
				a.res.SendAttached()
			}
		}
	}
}

// stopCaptureOnOutput ends the session of an unplugged output without waiting for core.
func (s *Server) stopCaptureOnOutput(name string) {
	ps := s.private.session
	if ps == nil || ps.output != name {
		return
	}
	s.log.Info().Uint64("session", ps.id).Str("reason", string(ports.CaptureReasonOutputGone)).Msg("capture session end")
	if ps.res.Alive() {
		ps.res.SendStopped(uint32(capturesession.NeferwlCaptureSessionV1StopReasonOutputGone))
	}
	ps.end(true)
}

// captureTag says how a capture of output o requested through life is
// served. The owner's connection gets clean captures of the session's output,
// fenced by the revision core last confirmed. While its session is unconfirmed,
// or after it ended and until the owner destroys the object, those captures
// are refused rather than served with the HUD in them. Every other connection,
// and the owner's captures of another output, are untouched.
func (s *Server) captureTag(life *server.Resource, o *output) (clean bool, session, revision uint64, ok bool) {
	if life == nil {
		return false, 0, 0, true
	}
	client := life.Client()
	if ps := s.private.session; ps != nil && client == ps.client && o.name() == ps.output {
		if !ps.confirmed || !ps.active {
			return false, 0, 0, false
		}
		return true, ps.id, ps.revision, true
	}
	for _, ps := range s.private.dead {
		if client == ps.client && o.name() == ps.output {
			return false, 0, 0, false
		}
	}
	return false, 0, 0, true
}
