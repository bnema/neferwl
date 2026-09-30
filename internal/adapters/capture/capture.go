// Package capture transfers each request to an output: the output closes its
// destination fd exactly once and attempts one reply, never blocking after
// its context is done.
package capture

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

const maxMappedBytes = 1 << 30

// Wayland SHM format identifiers; all capture pixels are opaque BGRA.
const (
	shmARGB8888 = 0
	shmXRGB8888 = 1
)

func reply(ctx context.Context, req ports.CaptureRequest, err error, replies chan<- ports.CaptureDone) {
	replyAt(ctx, req, err, monotonicNow(), replies)
}

func replyAt(ctx context.Context, req ports.CaptureRequest, err error, at time.Time, replies chan<- ports.CaptureDone) {
	if req.Dst.File != nil {
		err = errors.Join(err, req.Dst.File.Close())
	}
	done := ports.CaptureDone{ID: req.ID, Output: req.Output, Err: err, Time: at}
	// Prefer a ready reply even after output cancellation: Wayland needs the
	// completion to release admission credit while the session is still alive.
	select {
	case replies <- done:
		return
	default:
	}
	// A nil channel is allowed: cancellation still releases the sender.
	select {
	case <-ctx.Done():
	case replies <- done:
	}
}

// monotonicNow represents CLOCK_MONOTONIC in the Time seconds/nanoseconds fields.
func monotonicNow() time.Time {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return time.Time{}
	}
	return time.Unix(ts.Sec, ts.Nsec)
}

func Fail(ctx context.Context, req ports.CaptureRequest, err error, replies chan<- ports.CaptureDone) {
	reply(ctx, req, err, replies)
}

func writeFrame(ctx context.Context, req ports.CaptureRequest, frame ports.CaptureFrame, at time.Time, replies chan<- ports.CaptureDone) {
	var err error
	var data []byte
	// A client can truncate its file after Stat while it is still mapped.
	// Turn SIGBUS into an error on this worker, not a compositor crash.
	defer debug.SetPanicOnFault(debug.SetPanicOnFault(true))
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("capture read panic: %v\n%s", p, debug.Stack())
		}
		if data != nil {
			err = errors.Join(err, unix.Munmap(data))
		}
		replyAt(ctx, req, err, at, replies)
	}()
	if req.Dst.File == nil || req.Width <= 0 || req.Width > maxMappedBytes/4 || req.Height <= 0 || req.Stride < req.Width*4 || req.Region.Dx() != req.Width || req.Region.Dy() != req.Height || req.Dst.Offset < 0 || (req.Format != shmARGB8888 && req.Format != shmXRGB8888) {
		err = fmt.Errorf("invalid capture destination")
		return
	}
	rowBytes := int64(req.Width) * 4
	// Validate the bound before multiplication: padding must not wrap size.
	if int64(req.Dst.Offset) > maxMappedBytes-rowBytes || (req.Height > 1 && int64(req.Stride) > (maxMappedBytes-int64(req.Dst.Offset)-rowBytes)/int64(req.Height-1)) {
		err = fmt.Errorf("capture too large")
		return
	}
	size := int64(req.Dst.Offset) + int64(req.Height-1)*int64(req.Stride) + rowBytes
	if size <= 0 || size > int64(^uint(0)>>1) || size > maxMappedBytes {
		err = fmt.Errorf("capture too large")
		return
	}
	var st unix.Stat_t
	if e := unix.Fstat(int(req.Dst.File.Fd()), &st); e != nil {
		err = fmt.Errorf("fstat capture destination: %w", e)
		return
	}
	if st.Size < size {
		err = fmt.Errorf("capture buffer truncated")
		return
	}
	// Map only for the duration of this capture; the renderer copies BGRA rows
	// directly from its readback buffer into the selected shm region.
	data, err = unix.Mmap(int(req.Dst.File.Fd()), 0, int(size), unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return
	}
	err = frame.Read(req.Region, data[req.Dst.Offset:], req.Stride)
}
