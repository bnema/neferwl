package wayland

import (
	"errors"
	"image"
	"math"
	"os"
	"time"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	wlr "github.com/bnema/go-wayland-bindings/server/wlrscreencopy"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/server"
	"golang.org/x/sys/unix"
)

// maxCaptureInflight caps accepted captures that the backend has not completed
// yet, across all clients. Destroying a client frame or resource does not
// release its slot: only the backend completion does.
const maxCaptureInflight = ports.MaxCaptureInflight

// captureRegion maps a logical rectangle to target pixels: scaled to the
// scene-physical space, then through the output transform into the target
// (the framebuffer, mode size) that captures read.
func captureRegion(o *output, logical image.Rectangle) image.Rectangle {
	if o == nil {
		return image.Rectangle{}
	}
	scale := o.place.Scale
	if scale <= 0 {
		scale = 1
	}
	sw, sh := sceneSize(o)
	p := image.Rect(int(math.Floor(float64(logical.Min.X)*scale)), int(math.Floor(float64(logical.Min.Y)*scale)), int(math.Ceil(float64(logical.Max.X)*scale)), int(math.Ceil(float64(logical.Max.Y)*scale)))
	return o.place.Transform.RectToBuffer(p, sw, sh).Intersect(image.Rect(0, 0, o.place.Info.Width, o.place.Info.Height))
}

// sceneSize is the scene-physical size of the output: the mode size, swapped
// for a 90° or 270° transform.
func sceneSize(o *output) (int, int) {
	w, h := o.place.Info.Width, o.place.Info.Height
	if o.place.Transform.Rotated() {
		return h, w
	}
	return w, h
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
	return registerImageCopy(d, s)
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
	}, f.res.Resource, captureTag{taken: ports.CaptureFrameTaken{Output: f.o.name(), Region: logicalRegion(f.o, f.region)}})
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

// logicalRegion is a physical region of an output as the logical rectangle
// core marks: zero for the whole output, else the region's bounds rounded
// outwards to logical pixels.
func logicalRegion(o *output, phys image.Rectangle) ports.Rect {
	scale := o.place.Scale
	if scale <= 0 {
		scale = 1
	}
	if phys == image.Rect(0, 0, o.place.Info.Width, o.place.Info.Height) {
		return ports.Rect{}
	}
	phys = o.place.Transform.Invert().RectToBuffer(phys, o.place.Info.Width, o.place.Info.Height)
	x0, y0 := int(math.Floor(float64(phys.Min.X)/scale)), int(math.Floor(float64(phys.Min.Y)/scale))
	x1, y1 := int(math.Ceil(float64(phys.Max.X)/scale)), int(math.Ceil(float64(phys.Max.Y)/scale))
	return ports.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// captureTag says what a capture is for the renderer: the registered session
// (0 for wlr-screencopy), the exclusion fence and the workspace. taken is
// what core is told of the frame (ports.CaptureFrameTaken): the compositor
// shows an indicator for every capture, whatever the client or protocol.
type captureTag struct {
	session   uint64
	exclude   bool
	revision  uint64
	workspace uint64
	window    ports.WindowID
	offscreen bool
	taken     ports.CaptureFrameTaken
}

// requestCapture duplicates the pool descriptor, so destroying the wl_buffer
// cannot invalidate a capture in flight.
func (s *Server) requestCapture(o *output, rect image.Rectangle, cursor bool, b *wayland.Buffer, format uint32, reply func(ports.CaptureDone), life *server.Resource, tag captureTag) (uint64, bool) {
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
	// An ext frame (tag.session set) rides on the session's resolution.
	if life == nil || (tag.session != 0 && !s.mayCaptureFrame(life.Client())) || (tag.session == 0 && !s.mayCapture(life.Client())) {
		s.log.Debug().Str("output", o.name()).Msg("capture refused: client may not capture")
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
	taken := tag.taken
	taken.Session = tag.session
	s.captureReplies[id] = func(done ports.CaptureDone) {
		if done.Err == nil {
			// Whatever became of the client's frame object, its buffer holds
			// an image now: the indicator lasts CaptureFlash from here.
			s.emit(taken)
		}
		ev := s.log.Debug()
		if done.Err != nil && !errors.Is(done.Err, ports.ErrCaptureTransient) {
			ev = s.log.Info()
		}
		ev = ev.Err(done.Err)
		ev.Uint64("id", id).Str("output", done.Output).Dur("took", time.Since(start)).Bool("delivered", life.Alive()).Msg("capture done")
		if life.Alive() {
			reply(done)
		}
	}
	req := ports.CaptureRequest{ID: id, Output: o.name(), Region: rect, Cursor: cursor, Dst: ports.SHMBuffer{File: os.NewFile(uintptr(fd), "capture"), Offset: buf.offset}, Width: buf.width, Height: buf.height, Stride: buf.stride, Format: buf.format, Session: tag.session, Exclude: tag.exclude, CaptureRevision: tag.revision, Workspace: tag.workspace, Window: tag.window, OffScreen: tag.offscreen, Indicate: true}
	select {
	case s.channels.Captures <- req:
		s.log.Debug().Uint64("id", id).Str("output", o.name()).Stringer("region", rect).Uint32("format", format).Bool("cursor", cursor).Msg("capture requested")
		// The indicator is on screen while the frame is rendered.
		s.emit(taken)
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
				s.maybeLocked()
			}) {
				return
			}
		}
	}
}
