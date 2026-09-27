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
}

func (*screencopyFrame) Destroy(*wlr.ZwlrScreencopyFrameV1)                     {}
func (f *screencopyFrame) Copy(_ *wlr.ZwlrScreencopyFrameV1, b *wayland.Buffer) { f.copy(b, false) }
func (f *screencopyFrame) CopyWithDamage(_ *wlr.ZwlrScreencopyFrameV1, b *wayland.Buffer) {
	f.copy(b, true)
}
func (f *screencopyFrame) copy(b *wayland.Buffer, damage bool) {
	if f.used {
		return
	}
	f.used = true
	if f.o == nil || f.region.Empty() {
		f.res.SendFailed()
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
	_ = id
	if !ok {
		f.res.SendFailed()
	}
}

// requestCapture duplicates the pool descriptor, so destroying the wl_buffer
// cannot invalidate a capture in flight.
func (s *Server) requestCapture(o *output, rect image.Rectangle, cursor bool, b *wayland.Buffer, format uint32, reply func(ports.CaptureDone), life *server.Resource) (uint64, bool) {
	if o == nil || b == nil || s.channels.Captures == nil || s.channels.Captured == nil || s.outputByNameExact(o.name()) != o {
		return 0, false
	}
	buf, ok := s.buffers[b.Resource].(*buffer)
	if !ok || (format != uint32(wayland.ShmFormatArgb8888) && format != uint32(wayland.ShmFormatXrgb8888)) || buf.width != rect.Dx() || buf.height != rect.Dy() || buf.stride < rect.Dx()*4 || buf.format != format || !fileHolds(buf.pool.file, int64(buf.offset)+int64(buf.height-1)*int64(buf.stride)+int64(buf.width)*4) {
		return 0, false
	}
	fd, err := unix.Dup(int(buf.pool.file.Fd()))
	if err != nil {
		return 0, false
	}
	s.nextCapture++
	id := s.nextCapture
	s.captureReplies[id] = func(done ports.CaptureDone) {
		if life.Alive() {
			reply(done)
		}
	}
	req := ports.CaptureRequest{ID: id, Output: o.name(), Region: rect, Cursor: cursor, Dst: ports.SHMBuffer{File: os.NewFile(uintptr(fd), "capture"), Offset: buf.offset}, Width: buf.width, Height: buf.height, Stride: buf.stride, Format: buf.format}
	select {
	case s.channels.Captures <- req:
		return id, true
	default:
		delete(s.captureReplies, id)
		req.Dst.File.Close()
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
	res.OnDestroy = func() { delete(m.s.captureSessions, state) }
	if o == nil {
		res.SendStopped()
		state.stopped = true
		return
	}
	res.SendBufferSize(uint32(o.place.Info.Width), uint32(o.place.Info.Height))
	res.SendShmFormat(uint32(wayland.ShmFormatXrgb8888))
	res.SendShmFormat(uint32(wayland.ShmFormatArgb8888))
	res.SendDone()
}
func (m copyCaptureManager) CreatePointerCursorSession(r *ext.ExtImageCopyCaptureManagerV1, id uint32, _ *source.ExtImageCaptureSourceV1, _ *wayland.Pointer) {
	state := &cursorCaptureSession{}
	res, err := ext.NewExtImageCopyCaptureCursorSessionV1(r.Client(), 1, id, state)
	if err == nil {
		state.res = res
	}
}

type cursorCaptureSession struct {
	res *ext.ExtImageCopyCaptureCursorSessionV1
}

func (*cursorCaptureSession) Destroy(*ext.ExtImageCopyCaptureCursorSessionV1) {}
func (c *cursorCaptureSession) GetCaptureSession(_ *ext.ExtImageCopyCaptureCursorSessionV1, id uint32) {
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
}

func (*captureSession) Destroy(*ext.ExtImageCopyCaptureSessionV1) {}
func (c *captureSession) CreateFrame(r *ext.ExtImageCopyCaptureSessionV1, id uint32) {
	state := &captureExtFrame{session: c}
	res, err := ext.NewExtImageCopyCaptureFrameV1(r.Client(), 1, id, state)
	if err == nil {
		state.res = res
	}
}

type captureExtFrame struct {
	session *captureSession
	res     *ext.ExtImageCopyCaptureFrameV1
	buf     *wayland.Buffer
	used    bool
}

func (*captureExtFrame) Destroy(*ext.ExtImageCopyCaptureFrameV1) {}
func (f *captureExtFrame) AttachBuffer(_ *ext.ExtImageCopyCaptureFrameV1, b *wayland.Buffer) {
	f.buf = b
}
func (*captureExtFrame) DamageBuffer(_ *ext.ExtImageCopyCaptureFrameV1, _, _, _, _ int32) {}
func (f *captureExtFrame) Capture(*ext.ExtImageCopyCaptureFrameV1) {
	if f.used {
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
		f.res.SendFailed(uint32(ext.ExtImageCopyCaptureFrameV1FailureReasonBufferConstraints))
		return
	}
	buf, ok := c.s.buffers[f.buf.GetResource()].(*buffer)
	if !ok {
		f.res.SendFailed(uint32(ext.ExtImageCopyCaptureFrameV1FailureReasonBufferConstraints))
		return
	}
	_, ok = c.s.requestCapture(c.o, region, c.cursor, f.buf, buf.format, func(done ports.CaptureDone) {
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
		f.res.SendFailed(uint32(ext.ExtImageCopyCaptureFrameV1FailureReasonBufferConstraints))
	}
}
