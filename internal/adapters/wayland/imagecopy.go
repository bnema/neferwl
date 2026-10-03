package wayland

import (
	"image"
	"math"
	"time"

	source "github.com/bnema/go-wayland-bindings/server/extimagecapturesource"
	ext "github.com/bnema/go-wayland-bindings/server/extimagecopycapture"
	extworkspace "github.com/bnema/go-wayland-bindings/server/extworkspace"
	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/adapters/wayland/imagecapture"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
)

// ext-image-copy-capture with three kinds of source: an output (the standard
// ext-image-capture-source), and the workspace and output-region sources of
// neferwl_image_capture_source_manager_v1. Every session is registered with
// core (ports.CaptureSessionOpen), which decides its target, tells when it
// ends and draws the capture indicator while it lives; see ports/capture.go.

type sourceKind int

const (
	srcDead      sourceKind = iota // output or workspace gone: sessions stop at once
	srcOutput                      // a whole output
	srcRegion                      // a logical rectangle of an output
	srcWorkspace                   // a workspace frame
)

// captureSource is the state behind one ext_image_capture_source_v1.
type captureSource struct {
	kind      sourceKind
	output    *output
	region    image.Rectangle // output-local logical, srcRegion
	workspace uint64
}

func registerImageCopy(d *server.Display, s *Server) error {
	if err := source.NewExtOutputImageCaptureSourceManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = source.NewExtOutputImageCaptureSourceManagerV1(c, int32(v), id, outputCaptureSource{s})
	}); err != nil {
		return err
	}
	if err := imagecapture.NewNeferwlImageCaptureSourceManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = imagecapture.NewNeferwlImageCaptureSourceManagerV1(c, int32(v), id, extraSources{s})
	}); err != nil {
		return err
	}
	return ext.NewExtImageCopyCaptureManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = ext.NewExtImageCopyCaptureManagerV1(c, int32(v), id, copyCaptureManager{s})
	})
}

type captureSourceHandler struct{}

func (captureSourceHandler) Destroy(*source.ExtImageCaptureSourceV1) {}

// newSource makes the ext_image_capture_source_v1 of a source.
func (s *Server) newSource(c server.Client, id uint32, src *captureSource) {
	res, err := source.NewExtImageCaptureSourceV1(c, 1, id, captureSourceHandler{})
	if err != nil {
		return
	}
	s.captureSources[res.Resource] = src
	res.OnDestroy = func() { delete(s.captureSources, res.Resource) }
}

type outputCaptureSource struct{ s *Server }

func (outputCaptureSource) Destroy(*source.ExtOutputImageCaptureSourceManagerV1) {}
func (m outputCaptureSource) CreateSource(r *source.ExtOutputImageCaptureSourceManagerV1, id uint32, out *wayland.Output) {
	src := &captureSource{kind: srcDead}
	if o := m.s.outputOf(out); o != nil {
		src = &captureSource{kind: srcOutput, output: o}
	}
	m.s.newSource(r.Client(), id, src)
}

type extraSources struct{ s *Server }

func (extraSources) Destroy(*imagecapture.NeferwlImageCaptureSourceManagerV1) {}
func (m extraSources) CreateWorkspaceSource(r *imagecapture.NeferwlImageCaptureSourceManagerV1, id uint32, ws *extworkspace.ExtWorkspaceHandleV1) {
	src := &captureSource{kind: srcDead}
	if ws != nil {
		if wid, ok := m.s.workspaceIDOf(ws.Resource); ok {
			src = &captureSource{kind: srcWorkspace, workspace: wid}
		}
	}
	m.s.newSource(r.Client(), id, src)
}
func (m extraSources) CreateOutputRegionSource(r *imagecapture.NeferwlImageCaptureSourceManagerV1, id uint32, out *wayland.Output, x, y, w, h int32) {
	if w <= 0 || h <= 0 {
		r.PostError(uint32(imagecapture.NeferwlImageCaptureSourceManagerV1ErrorInvalidRegion), "width or height is not positive")
		return
	}
	src := &captureSource{kind: srcDead}
	if o := m.s.outputOf(out); o != nil {
		src = &captureSource{kind: srcRegion, output: o, region: image.Rect(int(x), int(y), int64Clamp(int64(x)+int64(w)), int64Clamp(int64(y)+int64(h)))}
	}
	m.s.newSource(r.Client(), id, src)
}

func (m extraSources) GetWorkspaceFrame(r *imagecapture.NeferwlImageCaptureSourceManagerV1, id uint32, ws *extworkspace.ExtWorkspaceHandleV1) {
	m.s.newWorkspaceFrame(r.Client(), id, ws)
}

// workspaceIDOf is the workspace behind a handle of any ext-workspace manager.
func (s *Server) workspaceIDOf(res *server.Resource) (uint64, bool) {
	for _, m := range s.workspaceManagers {
		for id, h := range m.handles {
			if h.res.Resource == res {
				return id, true
			}
		}
	}
	return 0, false
}

// workspaceInfo finds a workspace of the last snapshot core sent.
func (s *Server) workspaceInfo(id uint64) (output string, w ports.WorkspaceInfo, ok bool) {
	for _, out := range s.workspaceSnapshot.Outputs {
		for _, w := range out.Workspaces {
			if w.ID == id {
				return out.Name, w, true
			}
		}
	}
	return "", ports.WorkspaceInfo{}, false
}

type copyCaptureManager struct{ s *Server }

func (copyCaptureManager) Destroy(*ext.ExtImageCopyCaptureManagerV1) {}
func (m copyCaptureManager) CreateSession(r *ext.ExtImageCopyCaptureManagerV1, id uint32, src *source.ExtImageCaptureSourceV1, options uint32) {
	s := m.s
	if options&^1 != 0 {
		r.PostError(uint32(ext.ExtImageCopyCaptureManagerV1ErrorInvalidOption), "invalid capture option")
		return
	}
	var cs *captureSource
	if src != nil {
		cs = s.captureSources[src.Resource]
	}
	state := &captureSession{s: s, src: cs, cursor: options&1 != 0}
	res, err := ext.NewExtImageCopyCaptureSessionV1(r.Client(), 1, id, state)
	if err != nil {
		return
	}
	state.res = res
	s.captureSessions[state] = struct{}{}
	// Frames outlive their session (the spec): each frame drops its own reply.
	res.OnDestroy = func() { state.end(false, true) }
	if cs == nil || cs.kind == srcDead || !s.mayCapture(r.Client()) {
		state.end(true, false)
		return
	}
	if _, _, _, ok := state.geometry(); !ok {
		state.end(true, false)
		return
	}
	s.nextSession++
	state.id = s.nextSession
	s.sessionsByID[state.id] = state
	open := ports.CaptureSessionOpen{ID: state.id}
	switch cs.kind {
	case srcOutput:
		open.Output = cs.output.name()
	case srcRegion:
		open.Output, open.Region = cs.output.name(), ports.Rect{X: cs.region.Min.X, Y: cs.region.Min.Y, W: cs.region.Dx(), H: cs.region.Dy()}
	case srcWorkspace:
		open.Workspace = cs.workspace
	}
	s.emit(open)
	state.sendConstraints()
}

func (m copyCaptureManager) CreatePointerCursorSession(r *ext.ExtImageCopyCaptureManagerV1, id uint32, _ *source.ExtImageCaptureSourceV1, _ *wayland.Pointer) {
	state := &cursorCaptureSession{}
	res, err := ext.NewExtImageCopyCaptureCursorSessionV1(r.Client(), 1, id, state)
	if err == nil {
		state.res = res
	}
}

type cursorCaptureSession struct {
	res  *ext.ExtImageCopyCaptureCursorSessionV1
	used bool
}

func (*cursorCaptureSession) Destroy(*ext.ExtImageCopyCaptureCursorSessionV1) {}
func (c *cursorCaptureSession) GetCaptureSession(_ *ext.ExtImageCopyCaptureCursorSessionV1, id uint32) {
	if c.used {
		c.res.PostError(uint32(ext.ExtImageCopyCaptureCursorSessionV1ErrorDuplicateSession), "session already created")
		return
	}
	c.used = true
	state := &captureSession{stopped: true}
	res, err := ext.NewExtImageCopyCaptureSessionV1(c.res.Client(), 1, id, state)
	if err == nil {
		state.res = res
		res.SendStopped()
	}
}

// captureSession is one ext_image_copy_capture_session_v1.
type captureSession struct {
	s               *Server
	res             *ext.ExtImageCopyCaptureSessionV1
	src             *captureSource
	id              uint64 // registered with core; 0 when never
	cursor, stopped bool
	frame           *captureExtFrame
	sent            image.Point // buffer size of the last constraints
	// st is the last decision of core; haveState tells there is one.
	st        ports.CaptureSessionState
	haveState bool
	excl      *exclusion // the live exclusion object of this session
	// exclSeen: the last state of core knew an exclusion. Frames of an
	// excluded session leave the HUD out while it is (see tag).
	exclSeen bool
}

func (*captureSession) Destroy(*ext.ExtImageCopyCaptureSessionV1) {}

// end stops the session once. send tells the client (stopped), notify core
// (which has to forget it unless it decided the end itself).
func (c *captureSession) end(send, notify bool) {
	if c.stopped {
		return
	}
	c.stopped = true
	if send && c.res.Alive() {
		c.res.SendStopped()
	}
	if c.excl != nil {
		c.excl.end(false)
	}
	if c.id != 0 {
		delete(c.s.sessionsByID, c.id)
		if notify {
			c.s.emit(ports.CaptureSessionClose{ID: c.id})
		}
	}
	delete(c.s.captureSessions, c)
}

// geometry locates the session's frames now: the output, the physical region
// of the output they cover (for a workspace that is not on screen, the whole
// image of its off-screen render, from the origin) and whether the workspace
// is off screen. Core's last state decides where a workspace is; before it,
// the last workspace snapshot does.
func (c *captureSession) geometry() (o *output, rect image.Rectangle, hidden, ok bool) {
	src := c.src
	if src == nil {
		return nil, image.Rectangle{}, false, false
	}
	switch src.kind {
	case srcOutput:
		o = src.output
		if o == nil || c.s.outputByNameExact(o.name()) != o {
			return nil, image.Rectangle{}, false, false
		}
		return o, image.Rect(0, 0, o.place.Info.Width, o.place.Info.Height), false, true
	case srcRegion:
		o = src.output
		if o == nil || c.s.outputByNameExact(o.name()) != o {
			return nil, image.Rectangle{}, false, false
		}
		rect = captureRegion(o, src.region)
		return o, rect, false, !rect.Empty()
	case srcWorkspace:
		var name string
		var frame ports.Rect
		if c.haveState && c.st.Reason == ports.CaptureReasonNone {
			name, frame, hidden = c.st.Output, c.st.Rect, c.st.Hidden
		} else {
			out, w, found := c.s.workspaceInfo(src.workspace)
			if !found {
				return nil, image.Rectangle{}, false, false
			}
			name, frame, hidden = out, w.Frame, !w.Active
		}
		o = c.s.outputByNameExact(name)
		if o == nil || frame.W <= 0 || frame.H <= 0 {
			return nil, image.Rectangle{}, false, false
		}
		if hidden {
			scale := o.place.Scale
			if scale <= 0 {
				scale = 1
			}
			// The size of the off-screen child image (capture.childSize).
			return o, image.Rect(0, 0, int(math.Ceil(float64(frame.W)*scale)), int(math.Ceil(float64(frame.H)*scale))), true, true
		}
		rect = captureRegion(o, image.Rect(frame.X, frame.Y, frame.X+frame.W, frame.Y+frame.H))
		return o, rect, false, !rect.Empty()
	}
	return nil, image.Rectangle{}, false, false
}

// sendConstraints sends one complete constraint batch: size, formats, done.
func (c *captureSession) sendConstraints() {
	_, rect, _, ok := c.geometry()
	if !ok || !c.res.Alive() || c.s.protected() {
		return
	}
	c.sent = image.Pt(rect.Dx(), rect.Dy())
	c.res.SendBufferSize(uint32(rect.Dx()), uint32(rect.Dy()))
	c.res.SendShmFormat(uint32(wayland.ShmFormatXrgb8888))
	c.res.SendShmFormat(uint32(wayland.ShmFormatArgb8888))
	c.res.SendDone()
}

// refresh follows the target: new constraints when the buffer size changed,
// the end when nothing is left of it.
func (c *captureSession) refresh() {
	if c.stopped || c.id == 0 {
		return
	}
	if c.src.kind == srcWorkspace {
		// The workspace left the snapshot: its handle is removed, stop at once
		// without waiting for core's state.
		if _, _, found := c.s.workspaceInfo(c.src.workspace); !found {
			c.s.log.Info().Uint64("session", c.id).Str("reason", string(ports.CaptureReasonWorkspaceGone)).Msg("capture session end")
			c.end(true, true)
			return
		}
	}
	_, rect, _, ok := c.geometry()
	switch {
	case !ok && c.src.kind != srcWorkspace:
		c.s.log.Info().Uint64("session", c.id).Msg("capture session end: target empty")
		c.end(true, true)
	case ok && image.Pt(rect.Dx(), rect.Dy()) != c.sent:
		c.sendConstraints()
	}
}

// refreshSessions runs when outputs, scales or workspaces changed.
func (s *Server) refreshSessions() {
	for c := range s.captureSessions {
		c.refresh()
	}
}

// endCaptureSessions stops every session, and its exclusion with it, when
// the session gets protected: a capture session never outlives the lock, and
// its client must ask for a new one after the unlock.
func (s *Server) endCaptureSessions() {
	for c := range s.captureSessions {
		if c.id != 0 {
			s.log.Info().Uint64("session", c.id).Str("reason", "session-protected").Msg("capture session end")
		}
		c.end(true, true)
	}
}

// outputGone ends the sessions of an unplugged output without waiting for
// core. Workspace sessions follow their workspace to its new output.
func (s *Server) outputGone(o *output) {
	for c := range s.captureSessions {
		if c.src != nil && c.src.output == o && (c.src.kind == srcOutput || c.src.kind == srcRegion) {
			s.log.Info().Uint64("session", c.id).Str("reason", string(ports.CaptureReasonOutputGone)).Msg("capture session end")
			c.end(true, true)
		}
	}
}

// captureState applies what core decided about a session.
func (s *Server) captureState(st ports.CaptureSessionState) {
	c := s.sessionsByID[st.ID]
	if c == nil {
		return
	}
	if st.Reason.Terminal() {
		s.log.Info().Uint64("session", c.id).Str("reason", string(st.Reason)).Msg("capture session end")
		c.end(true, false)
		return
	}
	c.st, c.haveState, c.exclSeen = st, true, st.Exclusion
	s.log.Debug().Uint64("session", c.id).Bool("active", st.Active).Bool("hidden", st.Hidden).Uint64("revision", st.Revision).Msg("capture session state")
	if ex := c.excl; ex != nil {
		ex.confirm(st.Layers)
	}
	c.refresh()
}

// tag says how a frame of this session is served. A session with an
// exclusion gets frames without its HUD, fenced by the revision core last
// confirmed; until core knows the exclusion, and while a finished one is
// still in the last state, a frame is not served rather than shown with the
// HUD in it (ok false).
func (c *captureSession) tag(hidden bool) (t captureTag, ok bool) {
	t = captureTag{session: c.id}
	if c.src.kind == srcWorkspace {
		t.workspace, t.offscreen = c.src.workspace, hidden
	}
	switch c.src.kind {
	case srcOutput:
		t.taken = ports.CaptureFrameTaken{Output: c.src.output.name()}
	case srcRegion:
		r := c.src.region
		t.taken = ports.CaptureFrameTaken{Output: c.src.output.name(), Region: ports.Rect{X: r.Min.X, Y: r.Min.Y, W: r.Dx(), H: r.Dy()}}
	case srcWorkspace:
		t.taken = ports.CaptureFrameTaken{Workspace: c.src.workspace}
	}
	if c.excl != nil && !c.exclSeen {
		return t, false
	}
	if c.exclSeen {
		t.exclude, t.revision = true, c.st.Revision
	}
	return t, true
}

func (c *captureSession) CreateFrame(r *ext.ExtImageCopyCaptureSessionV1, id uint32) {
	if c.frame != nil {
		r.PostError(uint32(ext.ExtImageCopyCaptureSessionV1ErrorDuplicateFrame), "frame still alive")
		return
	}
	state := &captureExtFrame{session: c}
	res, err := ext.NewExtImageCopyCaptureFrameV1(r.Client(), 1, id, state)
	if err == nil {
		state.res = res
		c.frame = state
		res.OnDestroy = func() {
			if c.frame == state {
				c.frame = nil
			}
			if c.s != nil {
				delete(c.s.captureReplies, state.replyID)
			}
		}
	}
}

type captureExtFrame struct {
	session *captureSession
	res     *ext.ExtImageCopyCaptureFrameV1
	buf     *wayland.Buffer
	used    bool
	replyID uint64
	// deadline ends the retries of a frame whose target is not ready yet.
	deadline time.Time
}

// A frame whose target is briefly unavailable (not active yet, rendered off
// screen by another session, its indicator or exclusion not on screen yet,
// a renderer slot busy) is tried again rather than failed: failed(unknown)
// ends the whole screencast in clients such as xdg-desktop-portal-wlr. Only
// a stopped session, a refused client or a wrong buffer fail at once.
const (
	captureRetryEvery = 16 * time.Millisecond
	captureRetryFor   = time.Second
)

func (*captureExtFrame) Destroy(*ext.ExtImageCopyCaptureFrameV1) {}
func (f *captureExtFrame) AttachBuffer(_ *ext.ExtImageCopyCaptureFrameV1, b *wayland.Buffer) {
	if f.used {
		f.res.PostError(uint32(ext.ExtImageCopyCaptureFrameV1ErrorAlreadyCaptured), "already captured")
		return
	}
	f.buf = b
}
func (f *captureExtFrame) DamageBuffer(_ *ext.ExtImageCopyCaptureFrameV1, x, y, w, h int32) {
	if f.used {
		f.res.PostError(uint32(ext.ExtImageCopyCaptureFrameV1ErrorAlreadyCaptured), "already captured")
		return
	}
	if x < 0 || y < 0 || w <= 0 || h <= 0 {
		f.res.PostError(uint32(ext.ExtImageCopyCaptureFrameV1ErrorInvalidBufferDamage), "invalid damage")
	}
}

// constraintsFailed fails the frame with buffer_constraints. The buffer was
// checked against the geometry computed now; when that differs from the last
// constraints the client got (the target moved or rescaled and the refresh has
// not run yet), a new batch goes first so the client can retry with the size
// the compositor expects.
func (f *captureExtFrame) constraintsFailed(region image.Rectangle) {
	if c := f.session; image.Pt(region.Dx(), region.Dy()) != c.sent {
		c.sendConstraints()
	}
	f.res.SendFailed(uint32(ext.ExtImageCopyCaptureFrameV1FailureReasonBufferConstraints))
}

func (f *captureExtFrame) Capture(*ext.ExtImageCopyCaptureFrameV1) {
	if f.used {
		f.res.PostError(uint32(ext.ExtImageCopyCaptureFrameV1ErrorAlreadyCaptured), "already captured")
		return
	}
	f.used = true
	c := f.session
	if c.stopped || c.src == nil || !c.s.mayCaptureFrame(f.res.Client()) {
		f.res.SendFailed(uint32(ext.ExtImageCopyCaptureFrameV1FailureReasonStopped))
		return
	}
	if f.buf == nil {
		f.res.PostError(uint32(ext.ExtImageCopyCaptureFrameV1ErrorNoBuffer), "no buffer attached")
		return
	}
	f.deadline = c.s.clock.Now().Add(captureRetryFor)
	f.attempt()
}

// retry tries the frame again shortly, or fails it past its deadline.
func (f *captureExtFrame) retry(why string) {
	s := f.session.s
	if !s.clock.Now().Before(f.deadline) {
		s.log.Debug().Uint64("session", f.session.id).Str("reason", why).Msg("capture frame failed after retries")
		f.res.SendFailed(uint32(ext.ExtImageCopyCaptureFrameV1FailureReasonUnknown))
		return
	}
	s.clock.AfterFunc(captureRetryEvery, func() { s.display.Do(f.attempt) })
}

// attempt serves the frame from the current target. Display goroutine only.
func (f *captureExtFrame) attempt() {
	if !f.res.Alive() {
		return
	}
	c := f.session
	if c.stopped || !c.s.mayCaptureFrame(f.res.Client()) {
		f.res.SendFailed(uint32(ext.ExtImageCopyCaptureFrameV1FailureReasonStopped))
		return
	}
	o, region, hidden, ok := c.geometry()
	if !ok || c.haveState && !c.st.Active {
		f.retry("target not ready")
		return
	}
	tag, ok := c.tag(hidden)
	buf, isBuf := c.s.buffers[f.buf.GetResource()].(*buffer)
	switch {
	case !isBuf:
		f.constraintsFailed(region)
		return
	case !ok:
		// The exclusion is not confirmed by core yet: nothing is served.
		f.retry("exclusion not confirmed")
		return
	}
	f.replyID, ok = c.s.requestCapture(o, region, c.cursor, f.buf, buf.format, func(done ports.CaptureDone) {
		if done.Err != nil {
			f.retry(done.Err.Error())
			return
		}
		f.res.SendTransform(0)
		f.res.SendDamage(0, 0, int32(region.Dx()), int32(region.Dy()))
		hi, lo, ns := captureTime(done.Time)
		f.res.SendPresentationTime(hi, lo, ns)
		f.res.SendReady()
	}, f.res.Resource, tag)
	if !ok {
		switch {
		case !c.s.validCaptureBuffer(f.buf, region, buf.format):
			f.constraintsFailed(region)
		case !c.s.mayCaptureFrame(f.res.Client()):
			f.res.SendFailed(uint32(ext.ExtImageCopyCaptureFrameV1FailureReasonStopped))
		default:
			// The renderer queue or the in-flight bound is full.
			f.retry("capture queue full")
		}
	}
}
