package wayland

import (
	"github.com/bnema/neferwl/internal/domain"
	"golang.org/x/sys/unix"
	"time"
)

// windowProc collects the bounded chain on first map so later sibling
// arrivals can meet ancestors retained by the first external window.
func (s *Server) windowProc(id domain.WindowID, pid int, remap bool) []domain.ProcNode {
	if remap {
		return s.knownProc[id]
	}
	if pid <= 1 {
		return nil
	}
	chain := s.processes.Chain(pid)
	if len(chain) == 0 {
		return chain
	}
	var boot unix.Timespec
	if unix.ClockGettime(unix.CLOCK_BOOTTIME, &boot) != nil {
		return chain
	}
	now := s.clock.Now()
	for i := range chain {
		chain[i].Started = processStarted(now, time.Duration(boot.Nano()), chain[i].Key.Start)
	}
	return chain
}

// Linux exports stat starttime in USER_HZ (100 ticks/s on supported Linux
// architectures), independent of kernel HZ; no cgo sysconf is needed.
func processStarted(now time.Time, boot time.Duration, ticks uint64) time.Time {
	return now.Add(time.Duration(ticks)*(time.Second/100) - boot)
}
