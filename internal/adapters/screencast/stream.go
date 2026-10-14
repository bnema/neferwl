package screencast

import (
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-pipewire/pipewire"
	"github.com/bnema/zerobus"
)

// stream is one cast of a session: a PipeWire node fed with frames that the
// wayland server renders. Everything here is touched by the owner goroutine
// only; the PipeWire handler runs on it too, inside Loop.Iterate.
type stream struct {
	used bool
	sess *session
	id   uint32 // number in the object path
	cast uint64 // ID in the ports.CastCommands
	path string

	// Target.
	output string
	window ports.WindowID
	cursor bool
	maxFPS int
	// Stream.Parameters: position and size in the compositor space.
	px, py, pw, ph int32

	// Lifecycle.
	needOpen  bool // CastOpen is yet to be sent
	opened    bool // CastOpen was sent
	needClose bool // CastClose is yet to be sent
	closing   bool // the session ended: free once no frame is in flight
	stopped   bool // CastStopped came: the wayland side is done

	// Negotiation.
	w, h       int // size of the target, from CastSize
	haveSize   bool
	needResize bool
	src        videoSource
	h2         streamHandler
	pwState    int32 // last state of the PipeWire stream; -2 before the first
	seenState  bool  // a state above UNCONNECTED was seen
	nodeID     uint32
	announced  bool // PipeWireStreamAdded was sent
	format     pipewire.VideoFormat
	fw, fh     int // size the format was negotiated at; 0 while renegotiating

	// Frames.
	dirty    bool
	forced   bool // the first frame after STREAMING is not paced by damage
	inflight bool
	buf      *pipewire.VideoBuffer
	lastReq  time.Time
	seq      uint64
	errs     int // consecutive failed frames
}

// streamHandler is the pipewire.VideoHandler of a stream. Its callbacks run
// inside Loop.Iterate: they only record, the pump acts.
type streamHandler struct{ st *stream }

func (h *streamHandler) StateChanged(state int32, nodeID uint32, _ string) {
	st := h.st
	st.pwState = state
	if nodeID != 0 {
		st.nodeID = nodeID
	}
	if state > pipewire.StreamStateUnconnected {
		st.seenState = true
	}
	if state == pipewire.StreamStateStreaming {
		st.forced = true
	}
}

func (h *streamHandler) FormatChanged(f pipewire.VideoFormat, w, hh, _ int) {
	h.st.format, h.st.fw, h.st.fh = f, w, hh
	// A new format shows a new picture: the first frame is not paced by damage.
	h.st.forced = true
}

// BuffersChanged: the next pump looks for a free buffer anyway.
func (h *streamHandler) BuffersChanged() {}

// close ends the stream on the wayland side; the slot is freed by reap once
// no frame is in flight.
func (st *stream) close() {
	if st.closing {
		return
	}
	st.closing = true
	st.needOpen = false
	st.needClose = st.opened && !st.stopped
}

// encodeParameters writes the a{sv} of Stream.Parameters.
func encodeParameters(e *zerobus.Encoder, st *stream) {
	a := e.BeginArray('{')
	e.Struct()
	e.Str("position")
	e.Variant("(ii)")
	e.Struct()
	e.Int32(st.px)
	e.Int32(st.py)
	e.Struct()
	e.Str("size")
	e.Variant("(ii)")
	e.Struct()
	e.Int32(st.pw)
	e.Int32(st.ph)
	e.EndArray(a)
}
