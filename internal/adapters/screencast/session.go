package screencast

import (
	"strconv"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// session is one org.gnome.Mutter.ScreenCast.Session. The sessions live in a
// fixed array of Service, so pointers into it (the streams' back references,
// the PipeWire handlers) stay valid.
type session struct {
	used  bool
	id    uint32
	path  string
	owner string // unique name that called CreateSession
	// started: Start was called, the streams were opened.
	started bool
	// dying: the session is closed to the bus; the slot is freed once every
	// stream released its PipeWire resources.
	dying   bool
	streams [maxStreams]stream
}

func (s *Service) sessionByID(id uint32) *session {
	for i := range s.sessions {
		if sess := &s.sessions[i]; sess.used && !sess.dying && sess.id == id {
			return sess
		}
	}
	return nil
}

func (s *Service) streamByID(id uint32) *stream {
	for i := range s.sessions {
		sess := &s.sessions[i]
		if !sess.used || sess.dying {
			continue
		}
		for j := range sess.streams {
			if st := &sess.streams[j]; st.used && st.id == id {
				return st
			}
		}
	}
	return nil
}

func (s *Service) forEachSession(f func(*session)) {
	for i := range s.sessions {
		if s.sessions[i].used {
			f(&s.sessions[i])
		}
	}
}

func (s *Service) forEachStream(f func(*stream)) {
	for i := range s.sessions {
		sess := &s.sessions[i]
		if !sess.used {
			continue
		}
		for j := range sess.streams {
			if sess.streams[j].used {
				f(&sess.streams[j])
			}
		}
	}
}

// createSession answers CreateSession.
func (s *Service) createSession(req *request) {
	if req.remote {
		s.replyErr(req, errNotSupported, "remote desktop sessions are not supported")
		return
	}
	var sess *session
	for i := range s.sessions {
		if !s.sessions[i].used {
			sess = &s.sessions[i]
			break
		}
	}
	if sess == nil {
		s.replyErr(req, errLimits, "too many screen cast sessions")
		return
	}
	s.nextSess++
	*sess = session{used: true, id: s.nextSess, owner: req.sender}
	sess.path = prefixSession + strconv.FormatUint(uint64(sess.id), 10)
	s.log.Info().Str("sender", req.sender).Str("session", sess.path).Msg("screen cast session created")
	if e := s.replyTo(req, "o"); e != nil {
		e.ObjectPath(sess.path)
		s.send()
	}
}

// record answers RecordMonitor and RecordWindow: it adds a stream.
func (s *Service) record(req *request, sess *session) {
	if sess.started {
		s.replyErr(req, errFailed, "session already started")
		return
	}
	var st *stream
	for i := range sess.streams {
		if !sess.streams[i].used {
			st = &sess.streams[i]
			break
		}
	}
	if st == nil {
		s.replyErr(req, errLimits, "too many streams in the session")
		return
	}
	*st = stream{used: true, sess: sess, cursor: req.cursor != 0, maxFPS: defaultFPS, pwState: -2}
	if req.method == mRecordWindow {
		if !s.haveWindow(domain.WindowID(req.window)) {
			*st = stream{}
			s.replyErr(req, errInvalidArgs, "no such window")
			return
		}
		st.window = domain.WindowID(req.window)
		// A window moves and resizes: the stream carries no position.
		st.px, st.py, st.pw, st.ph = 0, 0, 1, 1
	} else {
		o := s.outputByName(req.text)
		if o == nil {
			*st = stream{}
			s.replyErr(req, errInvalidArgs, "no such monitor")
			return
		}
		st.output = o.Info.Name
		st.px, st.py, st.pw, st.ph = int32(o.X), int32(o.Y), int32(o.Width), int32(o.Height)
		if o.Current >= 0 && o.Current < len(o.Modes) {
			if hz := o.Modes[o.Current].RefreshMilli / 1000; hz > 0 {
				st.maxFPS = min(hz, 240)
			}
		}
	}
	s.nextStrm++
	s.nextCast++
	st.id, st.cast = s.nextStrm, s.nextCast
	st.path = prefixStream + strconv.FormatUint(uint64(st.id), 10)
	st.h2 = streamHandler{st: st}
	if e := s.replyTo(req, "o"); e != nil {
		e.ObjectPath(st.path)
		s.send()
	}
}

// start answers Start: the streams are opened on the wayland side.
func (s *Service) start(req *request, sess *session) {
	if sess.started {
		s.replyErr(req, errFailed, "session already started")
		return
	}
	n := 0
	for i := range sess.streams {
		if sess.streams[i].used {
			sess.streams[i].needOpen = true
			n++
		}
	}
	if n == 0 {
		s.replyErr(req, errFailed, "session has no stream")
		return
	}
	sess.started = true
	s.reply(req)
}

// closeSession ends a session: its streams are closed on the wayland side
// and, with signal set, the owner is told. The slot is freed by reap.
func (s *Service) closeSession(sess *session, signal bool) {
	if sess.dying {
		return
	}
	sess.dying = true
	for i := range sess.streams {
		if st := &sess.streams[i]; st.used {
			st.close()
			if !st.opened && st.src == nil {
				*st = stream{}
			}
		}
	}
	if signal {
		s.emitTo(sess.owner, sess.path, ifaceSession, "Closed")
		s.send()
	}
	s.log.Info().Str("session", sess.path).Msg("screen cast session closed")
}

// outputByName finds an output of the inventory by connector.
func (s *Service) outputByName(name string) *ports.CastOutput {
	for i := range s.inv.Outputs {
		if s.inv.Outputs[i].Info.Name == name {
			return &s.inv.Outputs[i]
		}
	}
	return nil
}

func (s *Service) haveWindow(id domain.WindowID) bool {
	for i := range s.inv.Windows {
		if s.inv.Windows[i].ID == id {
			return true
		}
	}
	return false
}

// properties answers Properties.Get, GetAll and Set.
func (s *Service) properties(req *request, sess *session, st *stream) {
	if req.method == mSet {
		s.replyErr(req, errPropertyReadOnly, "no writable property")
		return
	}
	iface := ""
	switch req.obj {
	case objScreenCast:
		iface = ifaceScreenCast
	case objSession:
		iface = ifaceSession
	case objStream:
		iface = ifaceStream
	case objDisplayConfig:
		iface = ifaceDisplayConfig
	case objIntrospect:
		iface = ifaceIntrospect
	case objScreenshot:
		iface = ifaceScreenshot
	}
	if req.text != iface && !(req.method == mGetAll && req.text == "") {
		s.replyErr(req, errUnknownInterface, "no such interface")
		return
	}
	if req.method == mGetAll {
		e := s.replyTo(req, "a{sv}")
		if e == nil {
			return
		}
		a := e.BeginArray('{')
		switch req.obj {
		case objScreenCast:
			e.Struct()
			e.Str("Version")
			e.Variant("i")
			e.Int32(screenCastVersion)
		case objStream:
			e.Struct()
			e.Str("Parameters")
			e.Variant("a{sv}")
			encodeParameters(e, st)
		case objIntrospect:
			e.Struct()
			e.Str("version")
			e.Variant("u")
			e.Uint32(introspectVersion)
		}
		e.EndArray(a)
		s.send()
		return
	}
	switch {
	case req.obj == objScreenCast && req.text2 == "Version":
		if e := s.replyTo(req, "v"); e != nil {
			e.Variant("i")
			e.Int32(screenCastVersion)
			s.send()
		}
	case req.obj == objStream && req.text2 == "Parameters":
		if e := s.replyTo(req, "v"); e != nil {
			e.Variant("a{sv}")
			encodeParameters(e, st)
			s.send()
		}
	case req.obj == objIntrospect && req.text2 == "version":
		if e := s.replyTo(req, "v"); e != nil {
			e.Variant("u")
			e.Uint32(introspectVersion)
			s.send()
		}
	default:
		s.replyErr(req, errUnknownProperty, "no such property")
	}
}

// Versions served. Introspect version 2 would make x-d-p-g read
// AnimationsEnabled and ScreenSize, which are not served.
const (
	screenCastVersion = 4
	introspectVersion = 1
	defaultFPS        = 60
)

func itoa(n int) string     { return strconv.Itoa(n) }
func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 3, 64) }
