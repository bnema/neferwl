package drm

import (
	"os"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Flip tracing (--debug=drm-flip) logs every commit completion with the
// times needed to tell a late client fence from a late flip: all times
// are CLOCK_MONOTONIC, the clock of flip events and fence timestamps.

// SYNC_IOC_FILE_INFO = _IOWR('>', 4, struct sync_file_info)
const ioctlSyncFileInfo = 0xc0383e04

// syncFileInfo is struct sync_file_info.
type syncFileInfo struct {
	name      [32]byte
	status    int32
	flags     uint32
	numFences uint32
	pad       uint32
	fences    uint64
}

// syncFenceInfo is struct sync_fence_info.
type syncFenceInfo struct {
	objName, driverName [32]byte
	status              int32
	flags               uint32
	timestampNs         uint64
}

// fenceSignalledAt is when every fence of the sync file f signalled
// (0: not signalled yet or unknown).
func fenceSignalledAt(f *os.File) time.Duration {
	info := syncFileInfo{}
	if ioctl(int(f.Fd()), ioctlSyncFileInfo, unsafe.Pointer(&info)) != nil || info.numFences == 0 {
		return 0
	}
	fences := make([]syncFenceInfo, info.numFences)
	info.fences = uint64(uintptr(unsafe.Pointer(&fences[0])))
	err := ioctl(int(f.Fd()), ioctlSyncFileInfo, unsafe.Pointer(&info))
	runtime.KeepAlive(fences)
	if err != nil {
		return 0
	}
	var at uint64
	for _, fi := range fences {
		if fi.status != 1 {
			return 0
		}
		at = max(at, fi.timestampNs)
	}
	return time.Duration(at)
}

// fencesSignalledAt is the latest signal time of fs (0: none, or one
// unsignalled).
func fencesSignalledAt(fs []*os.File) time.Duration {
	var at time.Duration
	for _, f := range fs {
		t := fenceSignalledAt(f)
		if t == 0 {
			return 0
		}
		at = max(at, t)
	}
	return at
}

func monotonic() time.Duration {
	var ts unix.Timespec
	_ = unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
	return time.Duration(ts.Nano())
}

// traceFlip logs the completion ev of the commit f started at start.
// fenceAt is when its fences signalled (0: none or unknown).
func (o *Output) traceFlip(ev flipEvent, f pendingFrame, start time.Time, fenceAt time.Duration) {
	now := monotonic()
	commitAt := now - time.Since(start)
	e := o.log.Debug().Str("connector", o.conn.name).Bool("frame", f.frame).Bool("async", f.async).Bool("vrr", o.vrrOn).
		Bool("direct", f.zeroCopy != 0).Uint32("seq", ev.seq).
		Float64("commit_to_flip_ms", ms(ev.when-commitAt)).Float64("flip_to_read_ms", ms(now-ev.when)).
		Int("fences", len(f.fences)).Bool("fence_ready_at_commit", f.fenceReady)
	if fenceAt != 0 {
		e = e.Float64("commit_to_fence_ms", ms(fenceAt-commitAt)).Float64("fence_to_flip_ms", ms(ev.when-fenceAt))
	}
	if f.frame {
		if o.lastFlipAt != 0 {
			e = e.Float64("flip_interval_ms", ms(ev.when-o.lastFlipAt))
		}
		o.lastFlipAt = ev.when
	}
	e.Msg("flip")
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
