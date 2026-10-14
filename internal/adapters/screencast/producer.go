package screencast

import (
	"time"

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
	case ports.CastDamage:
		st.dirty = true
	case ports.CastSize:
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
	case ports.CastFrameDone:
		s.frameDone(st, ev)
	case ports.CastStopped:
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
	if !st.inflight || st.buf == nil || st.buf.Index != ev.Buf {
		s.log.Warn().Str("stream", st.path).Int("buf", ev.Buf).Msg("frame done for no frame in flight")
		return
	}
	st.inflight = false
	if ev.Err != nil || st.closing {
		s.giveBack(st)
		if ev.Err == nil {
			return
		}
		st.errs++
		st.dirty = true
		if !st.closing {
			s.log.Info().Str("stream", st.path).Err(ev.Err).Int("errors", st.errs).Msg("cast frame failed")
			if st.errs >= maxFrameErrors {
				s.closeSession(st.sess, true)
			}
		}
		return
	}
	st.errs = 0
	st.seq++
	var pts time.Duration
	if !ev.Time.IsZero() {
		pts = time.Duration(ev.Time.UnixNano())
	}
	b := st.buf
	st.buf = nil
	st.src.Queue(b, st.seq, pts, false)
	st.src.Trigger()
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
		cmd := ports.CastCommand{Kind: ports.CastOpen, ID: st.cast, Output: st.output, Window: st.window, Cursor: st.cursor}
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
	if st.inflight || st.pwState != pipewire.StreamStateStreaming || st.needResize ||
		st.fw != st.w || st.fh != st.h || st.fw == 0 {
		return
	}
	if !st.dirty && !st.forced {
		return
	}
	interval := time.Second / time.Duration(max(st.maxFPS, 1))
	if since := now.Sub(st.lastReq); !st.lastReq.IsZero() && since < interval {
		s.wait(interval - since)
		return
	}
	b := st.src.Dequeue()
	if b == nil {
		// Every buffer is with the consumer: look again shortly.
		s.wait(interval)
		return
	}
	if b.Width != st.w || b.Height != st.h || len(b.Data) < b.Stride*b.Height {
		st.src.Release(b)
		s.wait(interval)
		return
	}
	cmd := ports.CastCommand{Kind: ports.CastFrame, ID: st.cast, Buf: b.Index, Data: b.Data, Stride: b.Stride, Width: b.Width, Height: b.Height, Format: shmFormat(b.Format)}
	if !s.sendCmd(cmd) {
		st.src.Release(b)
		s.wait(interval)
		return
	}
	st.inflight, st.buf = true, b
	st.lastReq, st.dirty, st.forced = now, false, false
}

// pumpClosing finishes a stream of an ended session: CastClose, then the
// PipeWire node once no frame is in flight.
func (s *Service) pumpClosing(st *stream) {
	if st.needClose {
		if !s.sendCmd(ports.CastCommand{Kind: ports.CastClose, ID: st.cast}) {
			s.wait(retryOpen)
			return
		}
		st.needClose = false
	}
	if st.inflight {
		return
	}
	if st.src != nil {
		s.giveBack(st)
		st.src.Destroy()
		st.src = nil
	}
	*st = stream{}
}

// reap frees the dying sessions once every stream released its resources.
func (s *Service) reap() {
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
		}
	}
}

// streamsLive reports whether a stream still holds PipeWire resources or a
// lent buffer.
func (s *Service) streamsLive() bool {
	live := false
	s.forEachStream(func(st *stream) { live = live || st.src != nil || st.inflight })
	return live
}
