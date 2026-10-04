// Package sched configures latency-sensitive compositor threads.
package sched

import (
	"errors"
	"sync"

	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

var unavailable sync.Once

// Realtime sets SCHED_RR at its minimum priority on the calling locked OS thread.
// Children cannot inherit this scheduling policy.
func Realtime(log zerowrap.Logger) error {
	err := unix.SchedSetAttr(0, &unix.SchedAttr{
		Size: unix.SizeofSchedAttr, Policy: unix.SCHED_RR,
		Flags: unix.SCHED_FLAG_RESET_ON_FORK, Priority: 1,
	}, 0)
	if errors.Is(err, unix.EPERM) {
		unavailable.Do(func() {
			log.Info().Msg("realtime scheduling unavailable, grant CAP_SYS_NICE")
		})
		return nil
	}
	return err
}
