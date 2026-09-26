// Package syncfile waits for Linux sync files (dma-fence fds), such as the
// GPU fence a frame render returns.
package syncfile

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// pollSlice bounds one poll so a canceled ctx is seen.
const pollSlice = 100 // ms

// Wait blocks until f signals or ctx ends. A sync file is signalled when
// it polls readable; an error or invalid fd is reported, not taken as
// signalled.
func Wait(ctx context.Context, f *os.File) error {
	fds := []unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(fds, pollSlice)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case err != nil:
			return fmt.Errorf("poll sync file: %w", err)
		case n > 0 && fds[0].Revents&unix.POLLIN != 0:
			return nil
		case n > 0:
			return fmt.Errorf("sync file poll events %#x", fds[0].Revents)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}
