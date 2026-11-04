package wayland

import "github.com/bnema/neferwl/internal/domain"

// windowProc collects the bounded chain on first map so later sibling
// arrivals can meet ancestors retained by the first external window.
func (s *Server) windowProc(id domain.WindowID, pid int, remap bool) []domain.ProcNode {
	if remap {
		return s.knownProc[id]
	}
	if pid <= 1 {
		return nil
	}
	return s.processes.Chain(pid)
}
