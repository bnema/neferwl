package screencast

import (
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-pipewire/pipewire"
)

// The producer turns wayland's damage into PipeWire frames, one stream at a
// time and without allocating in steady state:
//
//	CastDamage -> dirty
//	pump:  STREAMING, dirty or first frame, no frame in flight, interval
//	       elapsed, a buffer free  -> Dequeue, CastFrame{Data: buffer}
//	CastFrameDone ok -> Queue, then Trigger (the order PipeWire needs)
//	CastFrameDone with an error -> Release, retry paced
//
// The buffer's mapping is lent to the wayland server until the CastFrameDone
// of that buffer, so the source is never resized or destroyed while a frame
// is in flight.
//
// A cast ends (the session signals Closed) on Stop, when the owner leaves
// the bus (NameOwnerChanged), when wayland stops the cast (output or window
// gone, lock), when the PipeWire stream goes to ERROR or back to
// UNCONNECTED, and when the service shuts down. It does not end when the
// consumer leaves: as in Mutter, the node then goes to PAUSED, no frame is
// requested while it is not STREAMING (a frame already in flight is
// released instead of queued), and the first frame after it is STREAMING
// again is forced. The D-Bus session stays up for the next consumer.

// wl_shm formats of the supported video formats.
const (
	shmARGB8888 = 0
	shmXRGB8888 = 1
)

// maxFrameErrors is how many frames in a row may fail before the stream
// stops.
const maxFrameErrors = 3

// retryOpen is how long a command waits when the wayland channel is full.
const retryOpen = 10 * time.Millisecond

func shmFormat(f pipewire.VideoFormat) uint32 {
	if f == pipewire.VideoFormatBGRA {
		return shmARGB8888
	}
	return shmXRGB8888
}

// streamOfCast finds the stream of a cast ID.
func (s *Service) streamOfCast(id uint64) *stream {
	for i := range s.sessions {
		sess := &s.sessions[i]
		if !sess.used {
			continue
		}
		for j := range sess.streams {
			if st := &sess.streams[j]; st.used && st.cast == id {
				return st
			}
		}
	}
	return nil
}

// onCastEvent applies one event of the wayland server.
func (s *Service) onCastEvent(ev *ports.CastEvent) {
	st := s.streamOfCast(ev.ID)
	if st == nil {
		return
	}
	switch ev.Kind {
	case domain.CastDamage:
		st.dirty = true
	case domain.CastSize:
		if st.closing {
			return
		}
		if st.haveSize && st.w == ev.Width && st.h == ev.Height {
			return
		}
		st.w, st.h, st.haveSize = ev.Width, ev.Height, true
		if st.src != nil {
			st.needResize = true
		}
	case domain.CastFrameDone:
		s.frameDone(st, ev)
	case domain.CastStopped:
		st.stopped, st.needClose = true, false
		if st.inflight {
			// Wayland orders CastStopped after the frame's CastFrameDone.
			s.log.Warn().Str("stream", st.path).Msg("cast stopped with a frame in flight")
			st.inflight = false
			s.giveBack(st)
		}
		if !st.closing {
			s.log.Info().Str("stream", st.path).Str("reason", string(ev.Reason)).Err(ev.Err).Msg("cast stopped")
			s.closeSession(st.sess, true)
		}
	}
}

// giveBack releases the buffer a frame borrowed.
func (s *Service) giveBack(st *stream) {
	if st.buf != nil && st.src != nil {
		st.src.Release(st.buf)
	}
	st.buf = nil
}

// frameDone settles the frame in flight: a complete image is queued and
// triggered, anything else gives the buffer back.
func (s *Service) frameDone(st *stream, ev *ports.CastEvent) {
	b := s.settleFrame(st, ev)
	if b == nil {
		return
	}
	if st.pwState != pipewire.StreamStateStreaming {
		// The consumer left while the frame was drawn: nobody wants it.
		// The picture is kept for the consumer that comes next.
		st.src.Release(b)
		st.dirty = true
		return
	}
	st.src.Queue(b, st.seq, st.pts, false)
	st.src.Trigger()
}

// settleFrame updates the stream for the CastFrameDone of its frame and
// returns the buffer to queue, nil when there is none. It touches no
// PipeWire object but Release on the failure path.
func (s *Service) settleFrame(st *stream, ev *ports.CastEvent) *pipewire.VideoBuffer {
	if !st.inflight || st.buf == nil || st.buf.Index != ev.Buf {
		s.log.Warn().Str("stream", st.path).Int("buf", ev.Buf).Msg("frame done for no frame in flight")
		return nil
	}
	st.inflight = false
	if ev.Err != nil || st.closing {
		s.giveBack(st)
		if ev.Err == nil {
			return nil
		}
		st.errs++
		st.dirty = true
		if !st.closing {
			s.log.Info().Str("stream", st.path).Err(ev.Err).Int("errors", st.errs).Msg("cast frame failed")
			if st.errs >= maxFrameErrors {
				s.closeSession(st.sess, true)
			}
		}
		return nil
	}
	st.errs = 0
	st.seq++
	st.pts = 0
	if !ev.Time.IsZero() {
		st.pts = time.Duration(ev.Time.UnixNano())
	}
	b := st.buf
	st.buf = nil
	return b
}

// pumpAll runs every stream: commands, negotiation and frames.
func (s *Service) pumpAll(now time.Time) {
	s.waitFor = 0
	for i := range s.sessions {
		sess := &s.sessions[i]
		if !sess.used {
			continue
		}
		for j := range sess.streams {
			if st := &sess.streams[j]; st.used {
				s.pump(st, now)
			}
		}
	}
}

// wait asks for a poll timeout no longer than d.
func (s *Service) wait(d time.Duration) {
	if d <= 0 {
		d = time.Millisecond
	}
	if s.waitFor == 0 || d < s.waitFor {
		s.waitFor = d
	}
}

// send puts a command on the wayland channel without blocking.
func (s *Service) sendCmd(cmd ports.CastCommand) bool {
	select {
	case s.cmds <- cmd:
		return true
	default:
		return false
	}
}

func (s *Service) pump(st *stream, now time.Time) {
	if st.closing {
		s.pumpClosing(st)
		return
	}
	if st.needOpen {
		cmd := ports.CastCommand{Kind: domain.CastOpen, ID: st.cast, Output: st.output, Window: st.window, Cursor: st.cursor}
		if !s.sendCmd(cmd) {
			s.wait(retryOpen)
			return
		}
		st.needOpen, st.opened = false, true
	}
	if !st.haveSize {
		return
	}
	if st.src == nil {
		s.openSource(st)
		if st.src == nil {
			return
		}
	}
	if st.pwState == pipewire.StreamStateError || st.pwState == pipewire.StreamStateUnconnected && st.seenState {
		s.log.Info().Str("stream", st.path).Int32("state", st.pwState).Msg("PipeWire stream ended")
		s.closeSession(st.sess, true)
		return
	}
	if st.needResize && !st.inflight {
		st.needResize = false
		st.fw, st.fh = 0, 0
		if err := st.src.Resize(st.w, st.h); err != nil {
			s.log.Warn().Err(err).Str("stream", st.path).Msg("PipeWire resize failed")
			s.closeSession(st.sess, true)
			return
		}
	}
	if !st.announced && st.pwState >= pipewire.StreamStatePaused && st.nodeID != 0 {
		st.announced = true
		s.conn.NewSignalTo(st.sess.owner, st.path, ifaceStream, "PipeWireStreamAdded", "u").Uint32(st.nodeID)
		s.send()
	}
	s.pumpFrame(st, now)
}

// openSource creates the PipeWire node once the target size is known.
func (s *Service) openSource(st *stream) {
	if s.pwc == nil {
		s.closeSession(st.sess, true)
		return
	}
	cfg := pipewire.VideoConfig{Name: "neferwl-screencast", Width: st.w, Height: st.h, MaxFPS: st.maxFPS, Formats: []pipewire.VideoFormat{pipewire.VideoFormatBGRx, pipewire.VideoFormatBGRA}}
	src, err := s.pwc.NewVideoSource(cfg, &st.h2)
	if err != nil {
		s.log.Warn().Err(err).Str("stream", st.path).Msg("PipeWire stream not created")
		s.closeSession(st.sess, true)
		return
	}
	st.src = src
}

// pumpFrame requests the next frame when one is due.
func (s *Service) pumpFrame(st *stream, now time.Time) {
	if !s.frameDue(st, now) {
		return
	}
	b := st.src.Dequeue()
	if b == nil {
		// Every buffer is with the consumer: look again shortly.
		s.wait(st.interval())
		return
	}
	if !s.issueFrame(st, b, now) {
		st.src.Release(b)
	}
}

// interval is the shortest time between two frames.
func (st *stream) interval() time.Duration {
	return time.Second / time.Duration(max(st.maxFPS, 1))
}

// frameDue reports whether a frame may be requested now: streaming, the
// negotiated size is the target's, something changed (or it is the first
// frame), none in flight and the interval has passed. When only the interval
// is missing, it asks for a poll timeout.
func (s *Service) frameDue(st *stream, now time.Time) bool {
	if st.inflight || st.pwState != pipewire.StreamStateStreaming || st.needResize ||
		st.fw != st.w || st.fh != st.h || st.fw == 0 {
		return false
	}
	if !st.dirty && !st.forced {
		return false
	}
	if since := now.Sub(st.lastReq); !st.lastReq.IsZero() && since < st.interval() {
		s.wait(st.interval() - since)
		return false
	}
	return true
}

// issueFrame lends buffer b to the wayland server. False: the buffer is not
// taken and stays the caller's.
func (s *Service) issueFrame(st *stream, b *pipewire.VideoBuffer, now time.Time) bool {
	if b.Width != st.w || b.Height != st.h || len(b.Data) < b.Stride*b.Height {
		s.wait(st.interval())
		return false
	}
	cmd := ports.CastCommand{Kind: domain.CastFrame, ID: st.cast, Buf: b.Index, Data: b.Data, Stride: b.Stride, Width: b.Width, Height: b.Height, Format: shmFormat(b.Format)}
	if !s.sendCmd(cmd) {
		s.wait(st.interval())
		return false
	}
	st.inflight, st.buf = true, b
	st.lastReq, st.dirty, st.forced = now, false, false
	return true
}

// pumpClosing finishes a stream of an ended session: CastClose, then the
// PipeWire node once no frame is in flight. Destroying the node does not
// wait for CastClose to be sent (only a frame in flight lends its buffer),
// but the slot is kept until it is.
func (s *Service) pumpClosing(st *stream) {
	if st.needClose {
		if s.sendCmd(ports.CastCommand{Kind: domain.CastClose, ID: st.cast}) {
			st.needClose = false
		} else {
			s.wait(retryOpen)
		}
	}
	if st.inflight {
		return
	}
	if st.src != nil {
		s.giveBack(st)
		st.src.Destroy()
		st.src = nil
	}
	if !st.needClose {
		*st = stream{}
	}
}

// reap frees the dying sessions once every stream released its resources.
func (s *Service) reap() {
	freed := false
	for i := range s.sessions {
		sess := &s.sessions[i]
		if !sess.used || !sess.dying {
			continue
		}
		live := false
		for j := range sess.streams {
			live = live || sess.streams[j].used
		}
		if !live {
			*sess = session{}
			freed = true
		}
	}
	if freed {
		signal(s.sessionsReaped)
	}
}

// streamCount is the number of streams that still hold something: a
// PipeWire source, a lent buffer or an unsent CastClose.
func (s *Service) streamCount() int {
	n := 0
	s.forEachStream(func(*stream) { n++ })
	return n
}

// streamsLive reports whether a stream still holds something to release.
func (s *Service) streamsLive() bool { return s.streamCount() > 0 }
