package screencast

import (
	"errors"
	"strconv"
	"strings"

	"github.com/bnema/zerobus"
)

// Object paths, interfaces and errors of the served contract.
const (
	pathScreenCast    = "/org/gnome/Mutter/ScreenCast"
	prefixSession     = pathScreenCast + "/Session/u"
	prefixStream      = pathScreenCast + "/Stream/u"
	pathDisplayConfig = "/org/gnome/Mutter/DisplayConfig"
	pathIntrospect    = "/org/gnome/Shell/Introspect"
	pathScreenshot    = "/org/gnome/Shell/Screenshot"

	ifaceScreenCast     = "org.gnome.Mutter.ScreenCast"
	ifaceSession        = ifaceScreenCast + ".Session"
	ifaceStream         = ifaceScreenCast + ".Stream"
	ifaceDisplayConfig  = "org.gnome.Mutter.DisplayConfig"
	ifaceIntrospect     = "org.gnome.Shell.Introspect"
	ifaceScreenshot     = "org.gnome.Shell.Screenshot"
	ifaceProps          = "org.freedesktop.DBus.Properties"
	ifacePeer           = "org.freedesktop.DBus.Peer"
	ifaceIntrospectable = "org.freedesktop.DBus.Introspectable"

	errAccessDenied     = "org.freedesktop.DBus.Error.AccessDenied"
	errLimits           = "org.freedesktop.DBus.Error.LimitsExceeded"
	errNotSupported     = "org.freedesktop.DBus.Error.NotSupported"
	errInvalidArgs      = "org.freedesktop.DBus.Error.InvalidArgs"
	errUnknownObject    = "org.freedesktop.DBus.Error.UnknownObject"
	errUnknownProperty  = "org.freedesktop.DBus.Error.UnknownProperty"
	errUnknownInterface = "org.freedesktop.DBus.Error.UnknownInterface"
	errPropertyReadOnly = "org.freedesktop.DBus.Error.PropertyReadOnly"
	errFailed           = "org.freedesktop.DBus.Error.Failed"
)

// objKind is the object a call is addressed to.
type objKind uint8

const (
	objNone objKind = iota
	objScreenCast
	objSession
	objStream
	objDisplayConfig
	objIntrospect
	objScreenshot
)

// method is a call the service understands, after its (path, interface,
// member) were resolved.
type method uint8

const (
	mNotSupported method = iota
	mCreateSession
	mStart
	mStop
	mRecordMonitor
	mRecordWindow
	mGet
	mGetAll
	mSet
	mPing
	mIntrospect
	mGetState
	mGetWindows
	mShot
	mShotArea
	mShotInteractive
	mUnknownObject
)

// request is a decoded method call. Every value is copied out of the receive
// buffer with a length cap, so the request can wait for the caller check and
// the reply can be built after other messages were read.
type request struct {
	sender  string
	serial  uint32
	noReply bool
	obj     objKind
	id      uint32 // session or stream number from the path
	method  method
	bad     bool // the body does not match the method

	text, text2 string // connector; Properties interface and name
	window      uint64
	cursor      uint32
	remote      bool // CreateSession: remote-desktop-session-id given
	x, y, w, h  int32
	flag        bool
}

// privileged reports the calls that read the caller's executable again.
func (r *request) privileged() bool {
	switch r.method {
	case mCreateSession, mStart, mShot, mShotArea, mShotInteractive:
		return true
	}
	return false
}

// capStr copies s, cut to maxArg bytes.
func capStr(s string) string {
	if len(s) > maxArg {
		s = s[:maxArg]
	}
	return strings.Clone(s)
}

// objectOf finds the object of a path. A served object needs its bus name.
func (s *Service) objectOf(path string) (objKind, uint32) {
	switch path {
	case pathScreenCast:
		return objScreenCast, 0
	case pathDisplayConfig:
		return objDisplayConfig, 0
	case pathIntrospect:
		return objIntrospect, 0
	case pathScreenshot:
		return objScreenshot, 0
	}
	if n, ok := strings.CutPrefix(path, prefixSession); ok {
		id, err := strconv.ParseUint(n, 10, 32)
		return objSession, uint32(id) | boolBit(err != nil)
	}
	if n, ok := strings.CutPrefix(path, prefixStream); ok {
		id, err := strconv.ParseUint(n, 10, 32)
		return objStream, uint32(id) | boolBit(err != nil)
	}
	return objNone, 0
}

// boolBit marks an unparsable number with an id no object has.
func boolBit(bad bool) uint32 {
	if bad {
		return 0xffffffff
	}
	return 0
}

// served reports whether the bus name of the object is ours.
func (s *Service) served(k objKind) bool {
	switch k {
	case objScreenCast, objSession, objStream:
		return s.owns[0]
	case objDisplayConfig:
		return s.owns[1]
	case objIntrospect:
		return s.owns[2]
	case objScreenshot:
		return s.owns[3]
	}
	return false
}

// decode reads a call completely: nothing of m is used after it returns.
func (s *Service) decode(m *zerobus.Message) request {
	req := request{sender: strings.Clone(m.Sender), serial: m.Serial, noReply: m.Flags&zerobus.FlagNoReplyExpected != 0}
	req.obj, req.id = s.objectOf(m.Path)
	if !s.served(req.obj) {
		req.method = mUnknownObject
		return req
	}
	r := m.Body()
	sig := m.Signature
	want := func(w string) bool {
		if sig != w {
			req.bad = true
			return false
		}
		return true
	}
	switch m.Interface {
	case ifaceProps:
		switch m.Member {
		case "Get":
			req.method = mGet
			if want("ss") {
				req.text, req.text2 = capStr(r.Str()), capStr(r.Str())
			}
		case "GetAll":
			req.method = mGetAll
			if want("s") {
				req.text = capStr(r.Str())
			}
		case "Set":
			req.method = mSet
		}
	case ifacePeer:
		if m.Member == "Ping" {
			req.method = mPing
			want("")
		}
	case ifaceIntrospectable:
		if m.Member == "Introspect" {
			req.method = mIntrospect
			want("")
		}
	case ifaceScreenCast:
		if req.obj == objScreenCast && m.Member == "CreateSession" {
			req.method = mCreateSession
			if want("a{sv}") {
				s.readProps(r, &req)
			}
		}
	case ifaceSession:
		if req.obj == objSession {
			switch m.Member {
			case "Start":
				req.method = mStart
				want("")
			case "Stop":
				req.method = mStop
				want("")
			case "RecordMonitor":
				req.method = mRecordMonitor
				if want("sa{sv}") {
					req.text = capStr(r.Str())
					s.readProps(r, &req)
				}
			case "RecordWindow":
				req.method = mRecordWindow
				if want("a{sv}") {
					s.readProps(r, &req)
				}
			}
		}
	case ifaceDisplayConfig:
		if req.obj == objDisplayConfig && m.Member == "GetCurrentState" {
			req.method = mGetState
			want("")
		}
	case ifaceIntrospect:
		if req.obj == objIntrospect && m.Member == "GetWindows" {
			req.method = mGetWindows
			want("")
		}
	case ifaceScreenshot:
		if req.obj == objScreenshot {
			switch m.Member {
			case "Screenshot":
				req.method = mShot
				if want("bbs") {
					_ = r.Bool()
					req.flag = r.Bool()
					_ = r.Str()
				}
			case "ScreenshotArea":
				req.method = mShotArea
				if want("iiiibs") {
					req.x, req.y, req.w, req.h = r.Int32(), r.Int32(), r.Int32(), r.Int32()
					_ = r.Bool()
					_ = r.Str()
				}
			case "InteractiveScreenshot":
				req.method = mShotInteractive
				want("")
			}
		}
	}
	if r.Err() != nil {
		req.bad = true
	}
	return req
}

// readProps reads an a{sv} and keeps the keys the service knows.
func (s *Service) readProps(r *zerobus.Reader, req *request) {
	end := r.Array('{')
	for r.More(end) {
		r.Struct()
		key := r.Str()
		sig := r.Variant()
		switch {
		case key == "cursor-mode" && sig == "u":
			req.cursor = r.Uint32()
		case key == "window-id" && sig == "t":
			req.window = r.Uint64()
		case key == "remote-desktop-session-id":
			req.remote = true
			r.Skip(sig)
		default:
			r.Skip(sig)
		}
	}
}

// onMessage handles one message from the bus.
func (s *Service) onMessage(m *zerobus.Message) {
	switch m.Type {
	case zerobus.TypeMethodCall:
		if m.Sender != "" {
			req := s.decode(m)
			s.admit(&req)
		}
	case zerobus.TypeMethodReturn, zerobus.TypeError:
		if m.Sender == busDaemon {
			s.onBusReply(m)
		}
	case zerobus.TypeSignal:
		if m.Sender == busDaemon && m.Interface == busDaemon && m.Member == "NameOwnerChanged" {
			r := m.Body()
			name := r.Str()
			_ = r.Str()
			owner := r.Str()
			if r.Err() == nil && owner == "" && strings.HasPrefix(name, ":") {
				s.onGone(name)
			}
		}
	}
}

// send sends the message that was built. A broken connection ends the
// attempt; any other failure drops that one message.
func (s *Service) send() {
	if _, err := s.conn.Send(); err != nil {
		if errors.Is(err, zerobus.ErrClosed) {
			s.fatal = err
			return
		}
		s.log.Warn().Err(err).Msg("message not sent")
	}
}

// replyTo builds the reply of req, or nil when the caller wants none.
func (s *Service) replyTo(req *request, sig string) *zerobus.Encoder {
	if req.noReply {
		return nil
	}
	return s.conn.NewReply(&zerobus.Message{Sender: req.sender, Serial: req.serial}, sig)
}

// reply sends an empty reply.
func (s *Service) reply(req *request) {
	if s.replyTo(req, "") != nil {
		s.send()
	}
}

// replyErr sends an error reply.
func (s *Service) replyErr(req *request, name, text string) {
	if req.noReply {
		return
	}
	s.conn.NewError(&zerobus.Message{Sender: req.sender, Serial: req.serial}, name, "s").Str(text)
	s.send()
}

// emit sends a signal to one peer.
func (s *Service) emitTo(dest, path, iface, member string) *zerobus.Encoder {
	return s.conn.NewSignalTo(dest, path, iface, member, "")
}

// dispatch runs a call of a checked caller.
func (s *Service) dispatch(c *caller, req *request) {
	if !s.allowed(c.exe) {
		s.deny(req)
		return
	}
	if req.privileged() && !s.recheck(c) {
		s.deny(req)
		return
	}
	switch {
	case req.method == mUnknownObject:
		s.replyErr(req, errUnknownObject, "no such object")
		return
	case req.bad:
		s.replyErr(req, errInvalidArgs, "unexpected arguments")
		return
	}
	var sess *session
	var st *stream
	switch req.obj {
	case objSession:
		if sess = s.sessionByID(req.id); sess == nil {
			s.replyErr(req, errUnknownObject, "no such session")
			return
		}
	case objStream:
		if st = s.streamByID(req.id); st == nil {
			s.replyErr(req, errUnknownObject, "no such stream")
			return
		}
		sess = st.sess
	}
	if sess != nil && sess.owner != req.sender {
		s.deny(req)
		return
	}
	switch req.method {
	case mPing:
		s.reply(req)
	case mIntrospect:
		if e := s.replyTo(req, "s"); e != nil {
			e.Str("<node/>")
			s.send()
		}
	case mGet, mGetAll, mSet:
		s.properties(req, sess, st)
	case mCreateSession:
		s.createSession(req)
	case mStart:
		s.start(req, sess)
	case mStop:
		s.reply(req)
		s.closeSession(sess, true)
	case mRecordMonitor, mRecordWindow:
		s.record(req, sess)
	case mGetState:
		s.replyState(req)
	case mGetWindows:
		s.replyWindows(req)
	case mShot, mShotArea, mShotInteractive:
		s.screenshot(req)
	default:
		s.replyErr(req, errNotSupported, "not supported")
	}
}

// deny refuses a call.
func (s *Service) deny(req *request) {
	s.replyErr(req, errAccessDenied, "caller not allowed to capture the screen")
}
