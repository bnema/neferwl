package wayland

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"os"
	"slices"

	"github.com/bnema/neferwl/internal/adapters/wayland/imagecapture"
	"github.com/bnema/neferwl/internal/ports"
	ext "github.com/bnema/purego-libwayland/protocol/extimagecopycapture"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	"github.com/bnema/purego-libwayland/server"
)

// Capture exclusion (neferwl_capture_exclusion_manager_v1). A recorder asks
// for the exclusion of its ext-image-copy-capture session and gets a token;
// its HUD, usually on another connection of the same user, attaches layer
// surfaces with it. Core owns the decisions (ports/capture.go): this file
// relays requests and tells clients what core decided. Only one exclusion is
// live in the compositor, and it ends with its session.
//
// Privacy: the token never reaches a log, and neither does any peer
// identity; only lifecycle events are logged.

// exclusion is the state behind one neferwl_capture_exclusion_v1.
type exclusion struct {
	s       *Server
	res     *imagecapture.NeferwlCaptureExclusionV1
	session *captureSession
	token   string
	// layers are the attached layer surfaces; attached their attachment
	// objects, pending until a core state lists the layer.
	layers   []*layerSurface
	attached []*layerAttachment
	ended    bool
}

// layerAttachment is one neferwl_capture_layer_v1: it reports the result of
// an attach_surface and the layer's end.
type layerAttachment struct {
	res       *imagecapture.NeferwlCaptureLayerV1
	layer     *layerSurface
	confirmed bool // attached sent
}

func (a *layerAttachment) fail(reason imagecapture.NeferwlCaptureLayerV1Failure) {
	if a.res.Alive() {
		a.res.SendFailed(uint32(reason))
	}
}

// finish ends an attachment: detached after attached, failed before it.
func (a *layerAttachment) finish(failure imagecapture.NeferwlCaptureLayerV1Failure, detach imagecapture.NeferwlCaptureLayerV1DetachReason) {
	if !a.res.Alive() {
		return
	}
	if a.confirmed {
		a.res.SendDetached(uint32(detach))
	} else {
		a.res.SendFailed(uint32(failure))
	}
}

// trustedPeer reports whether a peer with these credentials may attach
// surfaces: its credentials are readable and it runs as the compositor's own
// user.
func trustedPeer(own uint32, peer server.Credentials, err error) bool {
	return err == nil && peer.UID == own
}

func (s *Server) trusted(c server.Client) bool {
	cr, err := c.Credentials()
	return trustedPeer(s.ownUID, cr, err)
}

func registerExclusion(d *server.Display, s *Server) error {
	s.ownUID = uint32(os.Geteuid())
	return imagecapture.NewNeferwlCaptureExclusionManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		m := &exclusionManager{s: s, trusted: s.trusted(c)}
		_, _ = imagecapture.NewNeferwlCaptureExclusionManagerV1(c, int32(v), id, m)
	})
}

type exclusionManager struct {
	s       *Server
	trusted bool
}

func (*exclusionManager) Destroy(*imagecapture.NeferwlCaptureExclusionManagerV1) {}

// sessionOf finds the registered session behind an ext session object.
func (s *Server) sessionOf(res *ext.ExtImageCopyCaptureSessionV1) *captureSession {
	if res == nil {
		return nil
	}
	for c := range s.captureSessions {
		if c.res != nil && c.res.Resource == res.Resource {
			return c
		}
	}
	return nil
}

func (m *exclusionManager) GetExclusion(r *imagecapture.NeferwlCaptureExclusionManagerV1, id uint32, session *ext.ExtImageCopyCaptureSessionV1) {
	s := m.s
	c := s.sessionOf(session)
	ex := &exclusion{s: s, session: c}
	res, err := imagecapture.NewNeferwlCaptureExclusionV1(r.Client(), r.Version(), id, ex)
	if err != nil {
		return
	}
	ex.res = res
	if c != nil && c.excl != nil {
		r.PostError(uint32(imagecapture.NeferwlCaptureExclusionManagerV1ErrorAlreadyExcluded), "the capture session already has an exclusion")
		return
	}
	switch {
	case c == nil || c.stopped || c.id == 0:
		ex.session = nil
		ex.ended = true
		res.SendFailed(uint32(imagecapture.NeferwlCaptureExclusionV1FailureSessionStopped))
		return
	case s.excl != nil:
		ex.session = nil
		ex.ended = true
		s.log.Info().Str("reason", "busy").Msg("capture exclusion refused")
		res.SendFailed(uint32(imagecapture.NeferwlCaptureExclusionV1FailureBusy))
		return
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		s.log.Warn().Msg("capture exclusion refused: no randomness")
		ex.session, ex.ended = nil, true
		res.SendFailed(uint32(imagecapture.NeferwlCaptureExclusionV1FailureSessionStopped))
		return
	}
	ex.token = hex.EncodeToString(raw[:])
	s.excl, c.excl = ex, ex
	s.log.Info().Uint64("session", c.id).Msg("capture exclusion begin")
	s.emit(ports.CaptureExclusionBegin{Session: c.id})
	res.SendToken(ex.token)
}

// Destroy: the owner let go of the exclusion; the session lives on.
func (ex *exclusion) Destroy(*imagecapture.NeferwlCaptureExclusionV1) {
	if !ex.ended {
		ex.end(true)
	}
}

func (m *exclusionManager) AttachSurface(r *imagecapture.NeferwlCaptureExclusionManagerV1, id uint32, token string, surface *wayland.Surface) {
	s := m.s
	if surface == nil || s.surfaces[surface.Resource] == nil || surface.Resource.Client() != r.Client() {
		r.PostError(uint32(imagecapture.NeferwlCaptureExclusionManagerV1ErrorInvalidSurface), "surface is not yours")
		return
	}
	state := s.surfaces[surface.Resource]
	l := state.layer
	att := &layerAttachment{layer: l}
	res, err := imagecapture.NewNeferwlCaptureLayerV1(r.Client(), r.Version(), id, att)
	if err != nil {
		return
	}
	att.res = res
	// Same user as the compositor, and the owner of the exclusion passed the
	// same check by holding the token.
	ex := s.excl
	switch {
	case s.protected() || !m.trusted: // DISPLAY protection; the UID of this connection, checked once at bind
		att.fail(imagecapture.NeferwlCaptureLayerV1FailureUnauthorized)
		return
	case ex == nil || subtle.ConstantTimeCompare([]byte(token), []byte(ex.token)) != 1:
		att.fail(imagecapture.NeferwlCaptureLayerV1FailureUnknownToken)
		return
	case state.kind != roleLayer || l == nil:
		r.PostError(uint32(imagecapture.NeferwlCaptureExclusionManagerV1ErrorSurfaceRole), "not a layer surface")
		return
	case l.pending.layer != ports.LayerTop && l.pending.layer != ports.LayerOverlay:
		r.PostError(uint32(imagecapture.NeferwlCaptureExclusionManagerV1ErrorInvalidLayer), "layer must be top or overlay")
		return
	case l.pending.keyboard == 1:
		r.PostError(uint32(imagecapture.NeferwlCaptureExclusionManagerV1ErrorExclusiveKeyboard), "exclusive keyboard")
		return
	case l.mapped || l.closed:
		r.PostError(uint32(imagecapture.NeferwlCaptureExclusionManagerV1ErrorInvalidSurface), "already mapped")
		return
	case l.capture != nil:
		att.fail(imagecapture.NeferwlCaptureLayerV1FailureAlreadyAttached)
		return
	case len(ex.layers) >= ports.MaxExclusionLayers:
		att.fail(imagecapture.NeferwlCaptureLayerV1FailureTooManyLayers)
		return
	}
	l.capture, l.attachment = ex, att
	l.pending.keyboard, l.current.keyboard = 0, 0
	ex.layers = append(ex.layers, l)
	ex.attached = append(ex.attached, att)
	// Queued before any LayerChanged of this layer: core excludes it from
	// the first scene that shows it.
	s.emit(ports.CaptureExclusionLayer{Session: ex.session.id, Layer: l.id, Attached: true})
}

func (*layerAttachment) Destroy(*imagecapture.NeferwlCaptureLayerV1) {}

// end ends the exclusion: every attachment is told (detached, or failed
// before it was confirmed) and its layer surface is closed so none lingers
// into the next exclusion. notify tells core; it is false when the session
// goes (core forgets the exclusion with it, or decided the end itself).
func (ex *exclusion) end(notify bool) {
	if ex.ended {
		return
	}
	s := ex.s
	ex.ended = true
	if s.excl == ex {
		s.excl = nil
	}
	sess := ex.session
	if sess != nil {
		// exclSeen stays as core's last state says: core keeps the ended
		// exclusion's layers excluded while a screen lists them, and frames
		// keep asking for that exclusion (fenced by its revision) until a
		// state says it is gone.
		sess.excl = nil
	}
	layers, attached := ex.layers, ex.attached
	ex.layers, ex.attached = nil, nil
	for _, a := range attached {
		a.finish(imagecapture.NeferwlCaptureLayerV1FailureExclusionEnded, imagecapture.NeferwlCaptureLayerV1DetachReasonExclusionEnded)
	}
	for _, l := range layers {
		l.capture, l.attachment = nil, nil
		// Closing unmaps the surface: the LayerChanged that removes it is
		// queued before the CaptureExclusionEnd below.
		l.close()
	}
	if notify && sess != nil {
		s.log.Info().Uint64("session", sess.id).Msg("capture exclusion end")
		s.emit(ports.CaptureExclusionEnd{Session: sess.id})
	}
}

// confirm sends attached to the attachments core now lists: only then is it
// safe for the client to show the layer.
func (ex *exclusion) confirm(listed []ports.WindowID) {
	for _, a := range ex.attached {
		if !a.confirmed && slices.Contains(listed, a.layer.id) {
			a.confirmed = true
			if a.res.Alive() {
				a.res.SendAttached()
			}
		}
	}
}

// captureDetach drops a layer surface whose role is gone from its exclusion.
// The caller unmapped it first, so the LayerChanged that removes the layer is
// already queued: core keeps excluding the layer until it reads that.
func (l *layerSurface) captureDetach() {
	ex, att := l.capture, l.attachment
	if ex == nil {
		return
	}
	l.capture, l.attachment = nil, nil
	ex.layers = removeItem(ex.layers, l)
	ex.attached = removeItem(ex.attached, att)
	att.finish(imagecapture.NeferwlCaptureLayerV1FailureLayerDestroyed, imagecapture.NeferwlCaptureLayerV1DetachReasonLayerDestroyed)
	if !ex.ended && ex.session != nil {
		ex.s.emit(ports.CaptureExclusionLayer{Session: ex.session.id, Layer: l.id})
	}
}
