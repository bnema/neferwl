package wayland

import "github.com/bnema/neferwl/internal/domain"

// windowProc performs I/O only on first map. An own identity bootstraps the
// mapped count; ancestor traversal is useful only with another known toplevel.
func (s *Server) windowProc(id domain.WindowID, pid int, remap bool) []domain.ProcNode {
	if remap {
		return s.knownProc[id]
	}
	if pid <= 1 {
		return nil
	}
	return s.processes.Chain(pid, len(s.mappedProc) > 0)
}
