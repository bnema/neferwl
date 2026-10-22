package wayland

import (
	"slices"

	"golang.org/x/sys/unix"
)

// The text clipboard bridge between Wayland and the built-in XWM. An X
// client's selection becomes a clipSource like any other (xSource); a Wayland
// selection is exposed to X through exposeToX. The X protocol side lives in
// the xwm package; nothing of it reaches core or ports.

// xwmSelection is the XWM as the clipboard sees it.
type xwmSelection interface {
	// WaylandSelection: a Wayland client owns kind with these MIME types;
	// nil mimes: none does (or it is protected).
	WaylandSelection(kind int, mimes []string)
	// SelectionReceive hands the XWM the write end of a reader's pipe (it
	// closes it) to fill with the X selection as mime.
	SelectionReceive(kind int, mime string, fd int)
}

// xSource is the clipSource of an X-owned selection: no Wayland client, the
// data comes from the XWM. It is valid while it is the current X source.
func (s *Server) xSource(kind int, mimes []string) *clipSource {
	src := &clipSource{mimes: slices.Clone(mimes), used: true, x11: true}
	src.alive = func() bool { return s.xsel[kind] == src }
	src.cancel = func() {}
	src.send = func(mime string, fd int) {
		x := s.xwmSel
		if x == nil {
			return
		}
		// receive closes its fd after send: the XWM gets its own.
		dup, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return
		}
		x.SelectionReceive(kind, mime, dup)
	}
	return src
}

// setXSelection is the XWM's report: an X client owns kind (mimes), or none
// does. A Wayland owner that already replaced the X one is never clobbered.
func (s *Server) setXSelection(kind int, mimes []string) {
	if len(mimes) == 0 {
		cur := s.xsel[kind]
		s.xsel[kind] = nil
		if cur != nil && s.selections[kind] == cur {
			s.setSelection(kind, nil)
		}
		return
	}
	src := s.xSource(kind, mimes)
	s.xsel[kind] = src
	s.setSelection(kind, src)
}

// exposeToX tells the XWM what Wayland offers. An X source is not exposed
// back (it is X's own); nil withdraws.
func (s *Server) exposeToX(kind int, src *clipSource) {
	if s.xwmSel == nil {
		return
	}
	if src == nil || src.x11 {
		s.xwmSel.WaylandSelection(kind, nil)
		return
	}
	s.xwmSel.WaylandSelection(kind, src.mimes)
}

// xSelectionRead returns a non-blocking read end of the Wayland selection
// kind as mime, or -1.
func (s *Server) xSelectionRead(kind int, mime string) int {
	src := s.selections[kind]
	if src == nil || src.x11 || s.protected() || !src.alive() || !slices.Contains(src.mimes, mime) {
		return -1
	}
	var p [2]int
	if unix.Pipe2(p[:], unix.O_CLOEXEC) != nil {
		return -1
	}
	if unix.SetNonblock(p[0], true) != nil {
		unix.Close(p[0])
		unix.Close(p[1])
		return -1
	}
	src.send(mime, p[1])
	unix.Close(p[1])
	return p[0]
}
