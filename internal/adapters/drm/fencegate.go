package drm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/bnema/neferwl/internal/adapters/syncfile"
	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"
)

// Old nvidia-drm drivers mishandle IN_FENCE_FD: the flip does not wait for
// the fence and shows a frame the GPU still draws
// (NVIDIA/open-gpu-kernel-modules#622, fixed in 560.35.3). On those the
// output waits for each fence on the CPU and commits without it.

const (
	driverNVIDIA = "nvidia-drm"
	// nvidiaDRMVersionFile holds the loaded module's version.
	nvidiaDRMVersionFile = "/sys/module/nvidia_drm/version"
	// ioctlVersion is DRM_IOWR('d', 0x00, struct drm_version).
	ioctlVersion = 0xC0406400
)

// nvidiaFenceFixed is the first nvidia-drm version that honours IN_FENCE_FD.
var nvidiaFenceFixed = [3]int{560, 35, 3}

// drmVersion is struct drm_version.
type drmVersion struct {
	major, minor, patch int32
	nameLen             uint64
	name                unsafe.Pointer
	dateLen             uint64
	date                unsafe.Pointer
	descLen             uint64
	desc                unsafe.Pointer
}

// driverName is the kernel driver of the DRM device fd (DRM_IOCTL_VERSION).
func driverName(fd int) (string, error) {
	var buf [64]byte
	v := drmVersion{nameLen: uint64(len(buf)), name: unsafe.Pointer(&buf[0])}
	if err := ioctl(fd, ioctlVersion, unsafe.Pointer(&v)); err != nil {
		return "", err
	}
	return string(buf[:min(int(v.nameLen), len(buf))]), nil
}

// parseVersion reads a dotted version such as "560.35.03" (missing parts
// are 0). Parts are plain digits: no sign.
func parseVersion(s string) ([3]int, error) {
	var v [3]int
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) > len(v) {
		return v, fmt.Errorf("version %q", s)
	}
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 31)
		if err != nil {
			return v, fmt.Errorf("version %q", s)
		}
		v[i] = int(n)
	}
	return v, nil
}

// versionLess reports whether a < b.
func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// brokenInFence reports whether the driver may mishandle IN_FENCE_FD:
// nvidia-drm before 560.35.3, or whose version is unknown (version is the
// module version file's content, err its read error).
func brokenInFence(driver, version string, err error) bool {
	if driver != driverNVIDIA {
		return false
	}
	if err != nil {
		return true
	}
	v, perr := parseVersion(version)
	return perr != nil || versionLess(v, nvidiaFenceFixed)
}

// fenceGate decides, once per card, whether commits carry no IN_FENCE_FD.
func fenceGate(fd int, log zerowrap.Logger) bool {
	driver, err := driverName(fd)
	if err != nil {
		log.Debug().Err(err).Msg("drm driver name unavailable")
		return false
	}
	var version string
	var verr error
	if driver == driverNVIDIA {
		b, rerr := os.ReadFile(nvidiaDRMVersionFile)
		version, verr = string(b), rerr
	}
	if !brokenInFence(driver, version, verr) {
		return false
	}
	log.Warn().Err(verr).Str("driver", driver).Str("version", strings.TrimSpace(version)).Msg("nvidia-drm version unknown or before 560.35.3 mishandles IN_FENCE_FD: waiting for frame fences on the CPU")
	return true
}

// fenceProp sets IN_FENCE_FD on plane p to the fence, unless there is none
// or the fences are waited for on the CPU (waitFences).
func (o *Output) fenceProp(req *atomicReq, p *plane, fence *os.File) {
	if fence == nil || o.cpuFences {
		return
	}
	req.set(p.id, p.prop("IN_FENCE_FD"), uint64(fence.Fd()))
}

// fenceWaitLimit bounds the CPU wait of one commit, whatever its number of
// fences.
const fenceWaitLimit = time.Second

// fenceWarnEvery is the least time between two warnings about a fence wait
// that hit fenceWaitLimit.
const fenceWarnEvery = 10 * time.Second

// fencePoll polls one fence without blocking. fn is built once, so polling
// allocates nothing; the output goroutine owns it.
type fencePoll struct {
	fds   [1]unix.PollFd
	ready bool
	fn    func(fd uintptr)
}

func (p *fencePoll) init() {
	p.fn = func(fd uintptr) {
		p.fds[0] = unix.PollFd{Fd: int32(fd), Events: unix.POLLIN}
		n, err := unix.Poll(p.fds[:], 0)
		p.ready = err == nil && n > 0 && p.fds[0].Revents&unix.POLLIN != 0
	}
}

// clientFenceReady reports whether a client's acquire fence lets its buffer
// be shown by a plane this frame. Where fences are waited for on the CPU, a
// client fence that has not signalled must not stall the output: the buffer
// is composed instead (the renderer waits for it on the GPU). Without CPU
// waits, and for no fence, it is ready; with them, a fence that cannot be
// polled is not ready.
func (o *Output) clientFenceReady(acquire *os.File) bool {
	if !o.cpuFences || acquire == nil {
		return true
	}
	if o.fencePoll.fn == nil {
		o.fencePoll.init()
	}
	raw, err := acquire.SyscallConn()
	if err != nil {
		return false
	}
	o.fencePoll.ready = false
	// Control keeps the descriptor open for the call: Wayland closing its
	// file meanwhile cannot hand us a reused number.
	if raw.Control(o.fencePoll.fn) != nil {
		return false
	}
	return o.fencePoll.ready
}

// waitFences blocks until the fences signalled when the driver cannot wait
// for them in the commit, for at most fenceWaitLimit in all, or until the
// output stops. A client fence passed here (direct scanout) already
// signalled: planes take only such buffers (clientFenceReady), so it
// returns at once. A failed or timed out wait is not fatal: the frame may
// then show early, as it would on any such driver. A timeout is counted in
// the stats and warned about at most every fenceWarnEvery.
func (o *Output) waitFences(fences ...*os.File) {
	if !o.cpuFences {
		return
	}
	ctx := o.runContext
	if ctx == nil {
		ctx = context.Background()
	}
	limit := o.fenceLimit
	if limit == 0 {
		limit = fenceWaitLimit
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	for _, f := range fences {
		if f == nil {
			continue
		}
		err := syncfile.Wait(ctx, f)
		switch {
		case err == nil || errors.Is(err, context.Canceled):
		case errors.Is(err, context.DeadlineExceeded):
			o.flipStats.fenceTimeouts++
			if now := time.Now(); o.fenceWarnAt.IsZero() || now.Sub(o.fenceWarnAt) >= fenceWarnEvery {
				o.fenceWarnAt = now
				o.log.Warn().Str("connector", o.conn.name).Dur("limit", limit).Msg("frame fence did not signal in time; committing anyway")
			}
		default:
			o.log.Debug().Err(err).Str("connector", o.conn.name).Msg("wait for frame fence")
		}
	}
}
