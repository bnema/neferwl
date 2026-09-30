package wayland

import (
	"image"
	"math"
	"os"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	source "github.com/bnema/purego-libwayland/protocol/extimagecapturesource"
	ext "github.com/bnema/purego-libwayland/protocol/extimagecopycapture"
	"github.com/bnema/purego-libwayland/protocol/wayland"
	wlr "github.com/bnema/purego-libwayland/protocol/wlrscreencopy"
	"github.com/bnema/purego-libwayland/server"
	"golang.org/x/sys/unix"
)

// maxCaptureInflight caps accepted captures that the backend has not completed
// yet, across all clients. Destroying a client frame or resource does not
// release its slot: only the backend completion does.
const maxCaptureInflight = ports.MaxCaptureInflight

// The renderer's scene and wl_output modes are already in physical orientation;
// this compositor does not advertise rotated wl_output transforms.
func captureRegion(o *output, logical image.Rectangle) image.Rectangle {
	if o == nil {
		return image.Rectangle{}
	}
	scale := o.place.Scale
	if scale <= 0 {
		scale = 1
	}
	p := image.Rect(int(math.Floor(float64(logical.Min.X)*scale)), int(math.Floor(float64(logical.Min.Y)*scale)), int(math.Ceil(float64(logical.Max.X)*scale)), int(math.Ceil(float64(logical.Max.Y)*scale)))
	return p.Intersect(image.Rect(0, 0, o.place.Info.Width, o.place.Info.Height))
}
func captureTime(t time.Time) (uint32, uint32, uint32) {
	sec := uint64(t.Unix())
	return uint32(sec >> 32), uint32(sec), uint32(t.Nanosecond())
}
func registerCapture(d *server.Display, s *Server) error {
	if err := wlr.NewZwlrScreencopyManagerV1Global(d, 3, func(c server.Client, v, id uint32) {
		_, _ = wlr.NewZwlrScreencopyManagerV1(c, int32(v), id, screencopyManager{s})
	}); err != nil {
		return err
	}
	if err := source.NewExtOutputImageCaptureSourceManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = source.NewExtOutputImageCaptureSourceManagerV1(c, int32(v), id, outputCaptureSource{s})
	}); err != nil {
		return err
	}
	return ext.NewExtImageCopyCaptureManagerV1Global(d, 1, func(c server.Client, v, id uint32) {
		_, _ = ext.NewExtImageCopyCaptureManagerV1(c, int32(v), id, copyCaptureManager{s})
	})
}

type screencopyManager struct{ s *Server }

func (screencopyManager) Destroy(*wlr.ZwlrScreencopyManagerV1) {}
func (m screencopyManager) CaptureOutput(r *wlr.ZwlrScreencopyManagerV1, id uint32, cursor int32, out *wayland.Output) {
	o := m.s.outputOf(out)
	if o == nil {
		m.create(r, id, cursor, nil, image.Rectangle{})
		return
	}
	m.create(r, id, cursor, o, image.Rect(0, 0, o.place.Info.Width, o.place.Info.Height))
}
func (m screencopyManager) CaptureOutputRegion(r *wlr.ZwlrScreencopyManagerV1, id uint32, cursor int32, out *wayland.Output, x, y, w, h int32) {
	o := m.s.outputOf(out)
	if w <= 0 || h <= 0 {
		m.create(r, id, cursor, o, image.Rectangle{})
		return
	}
	m.create(r, id, cursor, o, captureRegion(o, image.Rect(int(x), int(y), int64Clamp(int64(x)+int64(w)), int64Clamp(int64(y)+int64(h)))))
}
func int64Clamp(x int64) int {
	if x > int64(^uint(0)>>1) {
		return int(^uint(0) >> 1)
	}
	return int(x)
}
func (m screencopyManager) create(r *wlr.ZwlrScreencopyManagerV1, id uint32, cursor int32, o *output, rect image.Rectangle) {
	state := &screencopyFrame{s: m.s, o: o, region: rect, cursor: cursor != 0}
	f, err := wlr.NewZwlrScreencopyFrameV1(r.Client(), r.Version(), id, state)
	if err != nil {
		return
	}
	state.res = f
	if rect.Empty() {
		f.SendFailed()
		state.used = true
		return
	}
	f.SendBuffer(uint32(wayland.ShmFormatXrgb8888), uint32(rect.Dx()), uint32(rect.Dy()), uint32(rect.Dx()*4))
	if f.Version() >= 3 {
		f.SendBufferDone()
	}
}

type screencopyFrame struct {
	s            *Server
	res          *wlr.ZwlrScreencopyFrameV1
	o            *output
	region       image.Rectangle
	cursor, used bool
	replyID      uint64
}

func (f *screencopyFrame) Destroy(*wlr.ZwlrScreencopyFrameV1)                   { delete(f.s.captureReplies, f.replyID) }
func (f *screencopyFrame) Copy(_ *wlr.ZwlrScreencopyFrameV1, b *wayland.Buffer) { f.copy(b, false) }
func (f *screencopyFrame) CopyWithDamage(_ *wlr.ZwlrScreencopyFrameV1, b *wayland.Buffer) {
	f.copy(b, true)
}
func (f *screencopyFrame) copy(b *wayland.Buffer, damage bool) {
	if f.used {
		f.res.PostError(uint32(wlr.ZwlrScreencopyFrameV1ErrorAlreadyUsed), "frame already used")
		return
	}
	f.used = true
	if f.o == nil || f.region.Empty() || f.s.outputByNameExact(f.o.name()) != f.o {
		f.res.SendFailed()
		return
	}
	if !f.s.validCaptureBuffer(b, f.region, uint32(wayland.ShmFormatXrgb8888)) {
		f.res.PostError(uint32(wlr.ZwlrScreencopyFrameV1ErrorInvalidBuffer), "invalid shm buffer")
		return
	}
	id, ok := f.s.requestCapture(f.o, f.region, f.cursor, b, uint32(wayland.ShmFormatXrgb8888), func(done ports.CaptureDone) {
		if done.Err != nil {
			f.res.SendFailed()
			return
		}
		f.res.SendFlags(0)
		if damage && f.res.Version() >= 2 {
			f.res.SendDamage(0, 0, uint32(f.region.Dx()), uint32(f.region.Dy()))
		}
		hi, lo, ns := captureTime(done.Time)
		f.res.SendReady(hi, lo, ns)
	}, f.res.Resource)
	f.replyID = id
	if !ok {
		f.res.SendFailed()
	}
}

// validCaptureBuffer checks the advertised shm constraints before any request is queued.
func (s *Server) validCaptureBuffer(b *wayland.Buffer, rect image.Rectangle, format uint32) bool {
	if b == nil {
		return false
	}
	buf, ok := s.buffers[b.Resource].(*buffer)
	return ok && (format == uint32(wayland.ShmFormatArgb8888) || format == uint32(wayland.ShmFormatXrgb8888)) && buf.width == rect.Dx() && buf.height == rect.Dy() && buf.stride >= rect.Dx()*4 && buf.format == format && fileHolds(buf.pool.file, int64(buf.offset)+int64(buf.height-1)*int64(buf.stride)+int64(buf.width)*4)
}

// requestCapture duplicates the pool descriptor, so destroying the wl_buffer
// cannot invalidate a capture in flight.
func (s *Server) requestCapture(o *output, rect image.Rectangle, cursor bool, b *wayland.Buffer, format uint32, reply func(ports.CaptureDone), life *server.Resource) (uint64, bool) {
	if o == nil || b == nil || s.channels.Captures == nil || s.channels.Captured == nil || s.outputByNameExact(o.name()) != o {
		return 0, false
	}
	if !s.validCaptureBuffer(b, rect, format) {
		s.log.Debug().Str("output", o.name()).Stringer("region", rect).Uint32("format", format).Msg("capture buffer rejected")
		return 0, false
	}
	if len(s.captureInflight) >= maxCaptureInflight {
		s.log.Debug().Str("output", o.name()).Int("inflight", len(s.captureInflight)).Msg("capture refused: too many captures in flight")
		return 0, false
	}
	buf := s.buffers[b.Resource].(*buffer)
	fd, err := unix.Dup(int(buf.pool.file.Fd()))
	if err != nil {
		s.log.Warn().Err(err).Str("output", o.name()).Msg("capture: dup shm fd")
		return 0, false
	}
	s.nextCapture++
	id := s.nextCapture
	s.captureInflight[id] = struct{}{}
	start := time.Now()
	s.captureReplies[id] = func(done ports.CaptureDone) {
		ev := s.log.Debug()
		if done.Err != nil {
			ev = s.log.Info().Err(done.Err)
		}
		ev.Uint64("id", id).Str("output", done.Output).Dur("took", time.Since(start)).Bool("delivered", life.Alive()).Msg("capture done")
		if life.Alive() {
			reply(done)
		}
	}
	req := ports.CaptureRequest{ID: id, Output: o.name(), Region: rect, Cursor: cursor, Dst: ports.SHMBuffer{File: os.NewFile(uintptr(fd), "capture"), Offset: buf.offset}, Width: buf.width, Height: buf.height, Stride: buf.stride, Format: buf.format}
	select {
	case s.channels.Captures <- req:
		s.log.Debug().Uint64("id", id).Str("output", o.name()).Stringer("region", rect).Uint32("format", format).Bool("cursor", cursor).Msg("capture requested")
		return id, true
	default:
		delete(s.captureReplies, id)
		delete(s.captureInflight, id)
		req.Dst.File.Close()
		s.log.Debug().Str("output", o.name()).Msg("capture refused: request queue full")
		return 0, false
	}
}
func (s *Server) forwardCaptured(ctxDone <-chan struct{}) {
	for {
		select {
		case <-ctxDone:
			return
		case <-s.display.Stopped():
			return
		case done, ok := <-s.channels.Captured:
			if !ok {
				return
			}
			if !s.display.Do(func() {
				delete(s.captureInflight, done.ID)
				if fn := s.captureReplies[done.ID]; fn != nil {
					delete(s.captureReplies, done.ID)
					fn(done)
				}
			}) {
				return
			}
		}
	}
}

type outputCaptureSource struct{ s *Server }

func (outputCaptureSource) Destroy(*source.ExtOutputImageCaptureSourceManagerV1) {}
func (m outputCaptureSource) CreateSource(r *source.ExtOutputImageCaptureSourceManagerV1, id uint32, out *wayland.Output) {
	src, err := source.NewExtImageCaptureSourceV1(r.Client(), 1, id, captureSourceHandler{})
	if err != nil {
		return
	}
	m.s.captureSources[src.Resource] = m.s.outputOf(out)
	src.OnDestroy = func() { delete(m.s.captureSources, src.Resource) }
}

type captureSourceHandler struct{}

func (captureSourceHandler) Destroy(*source.ExtImageCaptureSourceV1) {}

type copyCaptureManager struct{ s *Server }

func (copyCaptureManager) Destroy(*ext.ExtImageCopyCaptureManagerV1) {}
func (m copyCaptureManager) CreateSession(r *ext.ExtImageCopyCaptureManagerV1, id uint32, src *source.ExtImageCaptureSourceV1, options uint32) {
	var o *output
	if options&^1 != 0 {
		r.PostError(uint32(ext.ExtImageCopyCaptureManagerV1ErrorInvalidOption), "invalid capture option")
		return
	}
	if src != nil {
		o = m.s.captureSources[src.Resource]
	}
	state := &captureSession{s: m.s, o: o, cursor: options&1 != 0}
	res, err := ext.NewExtImageCopyCaptureSessionV1(r.Client(), 1, id, state)
	if err != nil {
		return
	}
	state.res = res
	m.s.captureSessions[state] = struct{}{}
	// Frames outlive their session (the spec): each frame drops its own reply.
	res.OnDestroy = func() { delete(m.s.captureSessions, state) }
	if o == nil {
		res.SendStopped()
		state.stopped = true
		return
	}
	state.sendConstraints(o.place.Info.Width, o.place.Info.Height)
}

// sendConstraints sends one complete constraint batch: formats, size, done.
func (c *captureSession) sendConstraints(w, h int) {
	c.res.SendBufferSize(uint32(w), uint32(h))
	c.res.SendShmFormat(uint32(wayland.ShmFormatXrgb8888))
	c.res.SendShmFormat(uint32(wayland.ShmFormatArgb8888))
	c.res.SendDone()
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

type captureSession struct {
	s               *Server
	o               *output
	res             *ext.ExtImageCopyCaptureSessionV1
	cursor, stopped bool
	frame           *captureExtFrame
}

func (*captureSession) Destroy(*ext.ExtImageCopyCaptureSessionV1) {}
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
}

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
func (f *captureExtFrame) Capture(*ext.ExtImageCopyCaptureFrameV1) {
	if f.used {
		f.res.PostError(uint32(ext.ExtImageCopyCaptureFrameV1ErrorAlreadyCaptured), "already captured")
		return
	}
	f.used = true
	c := f.session
	if c.stopped || c.o == nil || c.s.outputByNameExact(c.o.name()) != c.o {
		f.res.SendFailed(uint32(ext.ExtImageCopyCaptureFrameV1FailureReasonStopped))
		return
	}
	region := image.Rect(0, 0, c.o.place.Info.Width, c.o.place.Info.Height)
	if f.buf == nil {
		f.res.PostError(uint32(ext.ExtImageCopyCaptureFrameV1ErrorNoBuffer), "no buffer attached")
		return
	}
	buf, ok := c.s.buffers[f.buf.GetResource()].(*buffer)
	if !ok {
		f.res.SendFailed(uint32(ext.ExtImageCopyCaptureFrameV1FailureReasonBufferConstraints))
		return
	}
	f.replyID, ok = c.s.requestCapture(c.o, region, c.cursor, f.buf, buf.format, func(done ports.CaptureDone) {
		if done.Err != nil {
			f.res.SendFailed(uint32(ext.ExtImageCopyCaptureFrameV1FailureReasonUnknown))
			return
		}
		f.res.SendTransform(0)
		f.res.SendDamage(0, 0, int32(region.Dx()), int32(region.Dy()))
		hi, lo, ns := captureTime(done.Time)
		f.res.SendPresentationTime(hi, lo, ns)
		f.res.SendReady()
	}, f.res.Resource)
	if !ok {
		if !c.s.validCaptureBuffer(f.buf, region, buf.format) {
			f.res.SendFailed(uint32(ext.ExtImageCopyCaptureFrameV1FailureReasonBufferConstraints))
		} else {
			f.res.SendFailed(uint32(ext.ExtImageCopyCaptureFrameV1FailureReasonUnknown))
		}
	}
}
