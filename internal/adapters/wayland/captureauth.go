package wayland

import "github.com/bnema/purego-libwayland/server"

// mayCapture is the one place that decides whether a client may capture the
// screen. Every capture path asks it: wlr-screencopy frames, and
// ext-image-copy-capture sessions and frames, whatever their source
// (output, output region or workspace). A refused client gets the failure
// its protocol defines (failed, or stopped for a session) and nothing is
// captured or flashed. While the session is protected (ext-session-lock) no
// client may capture, so nothing of the locked session reaches a buffer.
// Beyond that every client may capture; an allowlist of executables plugs in
// here.
func (s *Server) mayCapture(c server.Client) bool {
	return !s.protected()
}
