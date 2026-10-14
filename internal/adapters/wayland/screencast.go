package wayland

import (
	"errors"
	"image"
	"reflect"
	"slices"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

// Screencast. The screencast adapter (a D-Bus service that feeds PipeWire
// streams) drives casts through ports.CastCommand on Channels.CastCommands
// and hears back through Options.Casts (ports.CastSink). A cast is a capture
// target registered with core like an ext-image-copy-capture session
// (ports.CaptureSessionOpen), so the capture indicator stays on while it
// lives, but its frames are written straight into the PipeWire buffer the
// adapter lends (ports.CaptureRequest.Mapped) and it has no wayland client:
// the adapter checked its caller, so casts bypass mayCapture, not protected().
//
// Everything here runs on the display goroutine, which owns the casts. The
// frame cycle (CastFrame, the capture reply, CastFrameDone) and the damage
// events allocate nothing; opening a cast and publishing the inventory may.

// maxCasts bounds the live casts. Each has at most one frame in flight, and
// a frame takes one of the maxCaptureInflight admission credits like any
// capture, so capturedRingSize (MaxCaptureInflight + maxCasts) holds every
// reply.
const maxCasts = 8

// castRingSize bounds the cast commands waiting for the display goroutine.
const castRingSize = 64

var (
	errCastNoTarget  = errors.New("cast: target missing")
	errCastLimit     = errors.New("cast: too many casts")
	errCastProtected = errors.New("cast: session is protected")
	errCastStopped   = errors.New("cast: stopped")
	errCastNotReady  = errors.New("cast: target not ready")
	errCastSize      = errors.New("cast: frame size differs from the target")
	errCastBusy      = errors.New("cast: frame already in flight")
	errCastUnknown   = errors.New("cast: unknown cast")
)

// castSession is one cast.
type castSession struct {
	captureTarget
	cast   uint64 // the adapter's ID
	cursor bool
	// first: CaptureFrameTaken was sent, which turns the indicator on.
	first bool
	size  image.Point // the size the adapter was last told
	// dirty: the window committed since the last CastDamage (window casts).
	dirty   bool
	stopped bool
	// frame is the frame the adapter waits for (waiting) and, once its
	// capture was accepted, whether the output owns the buffer (inflight).
	frame      ports.CastCommand
	waiting    bool
	inflight   bool
	deadline   time.Time
	retryTimer ports.Timer
	// doneFn, retryFn and timerFn are bound once, so a frame binds no
	// closure: the capture reply, the retry on the display goroutine and the
	// timer callback that posts it.
	doneFn  func(ports.CaptureDone)
	retryFn func()
	timerFn func()
	// ending: the cast ended while a capture still wrote into the adapter's
	// buffer; CastStopped follows its reply.
	ending, announce bool
	endReason        ports.CaptureReason
	endErr           error
}

// postRetry is the retry timer's callback, on the timer's goroutine: it
// posts the retry to the display goroutine.
func (c *castSession) postRetry() { c.s.display.Post(c.retryFn) }

// push hands an event to the adapter.
func (c *castSession) push(ev ports.CastEvent) {
	ev.ID = c.cast
	c.s.pushCast(ev)
}

// pushCast sends ev to the sink; Push never blocks.
func (s *Server) pushCast(ev ports.CastEvent) {
	if s.casts != nil {
		s.casts.Push(ev)
	}
}

// applyCast runs one command of the adapter on the display goroutine.
func (s *Server) applyCast(cmd ports.CastCommand) {
	switch cmd.Kind {
	case ports.CastOpen:
		s.openCast(cmd)
	case ports.CastFrame:
		c := s.castSessions[cmd.ID]
		switch {
		case c == nil:
			s.refuseCastFrame(cmd, errCastUnknown)
		case c.waiting:
			s.refuseCastFrame(cmd, errCastBusy)
		default:
			c.frame, c.waiting = cmd, true
			c.deadline = s.clock.Now().Add(captureRetryFor)
			c.attempt()
		}
	case ports.CastClose:
		if c := s.castSessions[cmd.ID]; c != nil {
			c.end(ports.CaptureReasonNone, nil, true, false)
		}
	}
}

// refuseCastFrame answers a frame no cast takes, so the adapter gets its
// buffer back.
func (s *Server) refuseCastFrame(cmd ports.CastCommand, err error) {
	s.pushCast(ports.CastEvent{Kind: ports.CastFrameDone, ID: cmd.ID, Buf: cmd.Buf, Err: err})
}

// refuseCast ends a cast that never began.
func (s *Server) refuseCast(id uint64, reason ports.CaptureReason, err error) {
	s.log.Info().Uint64("cast", id).Str("reason", string(reason)).Err(err).Msg("cast refused")
	s.pushCast(ports.CastEvent{Kind: ports.CastStopped, ID: id, Reason: reason, Err: err})
}

// openCast registers a cast with core and tells the adapter its size.
func (s *Server) openCast(cmd ports.CastCommand) {
	switch {
	case s.castSessions[cmd.ID] != nil:
		s.log.Warn().Uint64("cast", cmd.ID).Msg("cast open: duplicate ID ignored")
		return
	case len(s.castSessions) >= maxCasts:
		s.refuseCast(cmd.ID, ports.CaptureReasonNone, errCastLimit)
		return
	case s.protected():
		s.refuseCast(cmd.ID, ports.CaptureReasonNone, errCastProtected)
		return
	}
	src := &captureSource{kind: srcDead}
	open := ports.CaptureSessionOpen{}
	gone := ports.CaptureReasonOutputGone
	if cmd.Window != 0 {
		gone = ports.CaptureReasonWindowGone
		if w := s.windows[cmd.Window]; w != nil && w.mapped && w.toplevel != nil {
			src = &captureSource{kind: srcWindow, window: cmd.Window, gen: w.gen}
			open.Window = cmd.Window
		}
	} else if o := s.outputByNameExact(cmd.Output); o != nil {
		src = &captureSource{kind: srcOutput, output: o}
		open.Output = o.name()
	}
	c := &castSession{captureTarget: captureTarget{s: s, src: src}, cast: cmd.ID, cursor: cmd.Cursor}
	if src.kind == srcDead {
		s.refuseCast(cmd.ID, gone, errCastNoTarget)
		return
	}
	_, rect, _, ok := c.geometry()
	if !ok {
		s.refuseCast(cmd.ID, gone, errCastNoTarget)
		return
	}
	c.doneFn, c.retryFn, c.timerFn = c.onDone, c.retryNow, c.postRetry
	s.nextSession++
	c.id = s.nextSession
	open.ID = c.id
	c.size = image.Pt(rect.Dx(), rect.Dy())
	s.castSessions[cmd.ID], s.castByCore[c.id] = c, c
	s.emit(open)
	s.log.Info().Uint64("cast", cmd.ID).Uint64("session", c.id).Int("w", c.size.X).Int("h", c.size.Y).Msg("cast open")
	c.push(ports.CastEvent{Kind: ports.CastSize, Width: c.size.X, Height: c.size.Y})
}

// state applies what core decided about the cast.
func (c *castSession) state(st ports.CaptureSessionState) {
	if c.applyState(st) {
		c.end(st.Reason, nil, false, true)
		return
	}
	c.refresh()
}

// refresh follows the target: a new size goes to the adapter, which has to
// renegotiate its stream; nothing left of the target ends the cast.
func (c *castSession) refresh() {
	if c.stopped {
		return
	}
	fate, rect, ok := c.follow()
	switch {
	case fate == targetWorkspaceGone:
		c.end(ports.CaptureReasonWorkspaceGone, nil, true, true)
	case fate == targetEmpty:
		c.s.log.Info().Uint64("cast", c.cast).Msg("cast end: target empty")
		c.end(ports.CaptureReasonOutputGone, nil, true, true)
	case ok && image.Pt(rect.Dx(), rect.Dy()) != c.size:
		c.size = image.Pt(rect.Dx(), rect.Dy())
		c.push(ports.CastEvent{Kind: ports.CastSize, Width: c.size.X, Height: c.size.Y})
	}
}

// end stops the cast once. notify tells core (which has to forget it unless
// it decided the end itself), announce tells the adapter (CastStopped; not
// for its own CastClose). A capture that still writes into the adapter's
// buffer finishes first: its CastFrameDone, then CastStopped, so CastStopped
// means the buffers are the adapter's again.
func (c *castSession) end(reason ports.CaptureReason, err error, notify, announce bool) {
	if c.stopped {
		return
	}
	s := c.s
	c.stopped = true
	if c.retryTimer != nil {
		c.retryTimer.Stop()
	}
	delete(s.castSessions, c.cast)
	delete(s.castByCore, c.id)
	if notify {
		s.emit(ports.CaptureSessionClose{ID: c.id})
	}
	c.announce, c.endReason, c.endErr = announce, reason, err
	if c.inflight {
		c.ending = true
		return
	}
	if c.waiting {
		c.frameDone(errCastStopped, time.Time{})
	}
	c.finish()
}

// finish tells the adapter the cast is over.
func (c *castSession) finish() {
	if c.announce {
		c.push(ports.CastEvent{Kind: ports.CastStopped, Reason: c.endReason, Err: c.endErr})
	}
}

// frameDone answers the frame the adapter waits for.
func (c *castSession) frameDone(err error, at time.Time) {
	c.waiting = false
	c.push(ports.CastEvent{Kind: ports.CastFrameDone, Buf: c.frame.Buf, Err: err, Time: at})
	c.frame = ports.CastCommand{}
}

// retryNow is the posted retry timer: the frame is tried again.
func (c *castSession) retryNow() {
	if !c.stopped && c.waiting && !c.inflight {
		c.attempt()
	}
}

// retry tries the frame again shortly, or fails it past its deadline, as a
// frame of an ext session does (captureExtFrame.retry).
func (c *castSession) retry(why error) {
	s := c.s
	if !s.clock.Now().Before(c.deadline) {
		s.log.Info().Uint64("cast", c.cast).Err(why).Msg("cast frame failed after retries")
		c.frameDone(why, time.Time{})
		return
	}
	if c.retryTimer == nil {
		c.retryTimer = s.clock.AfterFunc(captureRetryEvery, c.timerFn)
		return
	}
	c.retryTimer.Reset(captureRetryEvery)
}

// attempt serves the waiting frame from the target as it is now.
func (c *castSession) attempt() {
	s := c.s
	if s.protected() {
		c.end(ports.CaptureReasonNone, errCastProtected, true, true)
		return
	}
	o, rect, hidden, ok := c.geometry()
	if !ok || c.haveState && !c.st.Active {
		c.retry(errCastNotReady)
		return
	}
	if rect.Dx() != c.frame.Width || rect.Dy() != c.frame.Height {
		// The target moved on: the adapter has to renegotiate before it can
		// ask again; tell it now if refresh has not yet.
		c.refresh()
		if !c.stopped {
			c.frameDone(errCastSize, time.Time{})
		}
		return
	}
	if !s.requestCast(c, o, rect, c.targetTag(hidden)) {
		c.retry(ports.ErrCaptureBusy)
	}
}

// requestCast queues the capture of the waiting frame into the adapter's
// buffer. The reply is the cast's own prebound function, not a closure per
// frame. False: the in-flight bound or the renderer queue is full.
func (s *Server) requestCast(c *castSession, o *output, rect image.Rectangle, tag captureTag) bool {
	f := c.frame
	if o == nil || s.channels.Captures == nil || s.channels.Captured == nil || len(s.captureInflight) >= maxCaptureInflight {
		return false
	}
	s.nextCapture++
	id := s.nextCapture
	s.captureInflight[id] = struct{}{}
	s.captureReplies[id] = c.doneFn
	req := ports.CaptureRequest{ID: id, Output: o.name(), Region: rect, Cursor: c.cursor, Mapped: f.Data, Width: f.Width, Height: f.Height, Stride: f.Stride, Format: f.Format, Session: tag.session, Workspace: tag.workspace, Window: tag.window, OffScreen: tag.offscreen, Indicate: true}
	select {
	case s.channels.Captures <- req:
	default:
		delete(s.captureReplies, id)
		delete(s.captureInflight, id)
		return false
	}
	c.inflight = true
	if !c.first {
		// The indicator is on screen while the frame is rendered; the cast
		// keeps it on for as long as it lives.
		c.first = true
		taken := tag.taken
		taken.Session = tag.session
		s.emit(taken)
	}
	return true
}

// onDone settles the capture of the waiting frame.
func (c *castSession) onDone(done ports.CaptureDone) {
	c.inflight = false
	if errors.Is(done.Err, ports.ErrCaptureTransient) && !c.stopped {
		c.retry(done.Err)
		return
	}
	err := done.Err
	if err != nil {
		c.s.log.Info().Uint64("cast", c.cast).Err(err).Msg("cast frame failed")
	}
	if c.ending {
		// The cast is over: the adapter must not queue this frame.
		err = errors.Join(errCastStopped, err)
	}
	if c.waiting {
		c.frameDone(err, done.Time)
	}
	if c.ending {
		c.ending = false
		c.finish()
	}
}

// castFlip answers a flip of an output: the casts on it have something new
// to show. An output cast always has; a window cast only when its window
// committed since the last damage. Display goroutine; no allocation.
func (s *Server) castFlip(output string, f *ports.FlipInfo) {
	if len(s.castSessions) == 0 {
		return
	}
	var at time.Time
	for _, c := range s.castSessions {
		switch c.src.kind {
		case srcOutput:
			if c.src.output.name() != output {
				continue
			}
		case srcWindow:
			if !c.dirty {
				continue
			}
			if o, _, _, ok := c.geometry(); !ok || o.name() != output {
				continue
			}
		default:
			continue
		}
		if at.IsZero() {
			at = flipTime(f)
		}
		c.dirty = false
		c.push(ports.CastEvent{Kind: ports.CastDamage, Time: at})
	}
}

// flipTime is when a flip happened, on CLOCK_MONOTONIC as capture times are.
func flipTime(f *ports.FlipInfo) time.Time {
	if f.When > 0 {
		return time.Unix(0, int64(f.When))
	}
	var ts unix.Timespec
	if unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts) != nil {
		return time.Time{}
	}
	return time.Unix(ts.Sec, ts.Nsec)
}

// castCommitted marks the casts of a window that committed content.
func (s *Server) castCommitted(id ports.WindowID) {
	for _, c := range s.castSessions {
		if c.src.kind == srcWindow && c.src.window == id {
			c.dirty = true
		}
	}
}

// forwardCasts moves the adapter's commands to the display goroutine through
// castRing, in order; a full ring blocks this goroutine, never the display.
func (s *Server) forwardCasts(ctxDone <-chan struct{}) {
	for {
		select {
		case <-ctxDone:
			return
		case <-s.display.Stopped():
			return
		case cmd, ok := <-s.channels.CastCommands:
			if !ok {
				return
			}
			if !s.castRing.Push(cmd) {
				return
			}
		}
	}
}

// castInventoryChanged schedules a publication of the inventory after the
// outputs, the titles, the app ids or the mapped windows may have changed.
// Changes made in one display loop turn share one publication.
func (s *Server) castInventoryChanged() {
	if s.casts == nil || s.castInvPosted {
		return
	}
	s.castInvPosted = true
	s.display.Post(s.castInvFn)
}

// publishCastInventory sends the inventory when it differs from the last one.
func (s *Server) publishCastInventory() {
	s.castInvPosted = false
	inv := s.castInventory()
	if s.castInvSent && reflect.DeepEqual(inv, s.castInvLast) {
		return
	}
	// The receiver owns what it is given: keep a copy of our own to compare.
	s.castInvLast, s.castInvSent = inv, true
	s.casts.Inventory(s.castInventory())
}

// castInventory is the outputs and, unless the session is protected, the
// mapped toplevels now.
func (s *Server) castInventory() ports.CastInventory {
	inv := ports.CastInventory{Focused: s.focusedOutput, Outputs: make([]ports.CastOutput, 0, len(s.outputs))}
	for _, o := range s.outputs {
		p := o.place
		out := ports.CastOutput{Info: p.Info, Current: -1, X: p.X, Y: p.Y, Width: p.Width, Height: p.Height, Scale: p.Scale, Transform: p.Transform}
		for _, h := range s.outputHeads.Heads {
			if h.Info.Name == p.Info.Name {
				out.Modes = slices.Clone(h.Modes)
				if h.Current != nil {
					out.Current = slices.Index(out.Modes, *h.Current)
				}
			}
		}
		if out.Current < 0 {
			// No head inventory (headless) or the mode is not listed: the
			// mode the output runs.
			mode := ports.OutputMode{Width: p.Info.Width, Height: p.Info.Height, RefreshMilli: p.Info.RefreshMilli, Preferred: len(out.Modes) == 0}
			if out.Current = slices.IndexFunc(out.Modes, func(m ports.OutputMode) bool {
				return m.Width == mode.Width && m.Height == mode.Height && m.RefreshMilli == mode.RefreshMilli
			}); out.Current < 0 {
				out.Modes, out.Current = append(out.Modes, mode), len(out.Modes)
			}
		}
		inv.Outputs = append(inv.Outputs, out)
	}
	if s.protected() {
		return inv
	}
	for _, id := range s.mappedToplevels() {
		w := s.windows[id]
		inv.Windows = append(inv.Windows, ports.CastWindow{ID: id, AppID: w.appID, Title: w.title})
	}
	return inv
}
