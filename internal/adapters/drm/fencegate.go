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
// are 0).
func parseVersion(s string) ([3]int, error) {
	var v [3]int
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) == 0 || len(parts) > 3 || parts[0] == "" {
		return v, fmt.Errorf("version %q", s)
	}
	for i, p := range parts {
		// Digits only: Atoi would also take a sign.
		n, err := strconv.Atoi(p)
		if err != nil || p[0] < '0' || p[0] > '9' {
			return v, fmt.Errorf("version %q", s)
		}
		v[i] = n
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
	log.Warn().Err(verr).Str("driver", driver).Str("version", strings.TrimSpace(version)).Msg("nvidia-drm before 560.35.3 mishandles IN_FENCE_FD: waiting for frame fences on the CPU")
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

// fenceWaitLimit bounds a CPU wait on a fence of our own render.
const fenceWaitLimit = time.Second

// clientFenceReady reports whether a client's acquire fence lets its buffer
// be shown by a plane this frame. Where fences are waited for on the CPU, a
// client fence that has not signalled must not stall the output: the buffer
// is composed instead (the renderer waits for it on the GPU). A nil or
// unpollable fence counts as signalled, as in signalled.
func (o *Output) clientFenceReady(acquire *os.File) bool {
	if !o.cpuFences || acquire == nil {
		return true
	}
	// Wayland may close its file at any time: poll a duplicate.
	d := dupFence(acquire)
	if d == nil {
		return true
	}
	defer d.Close()
	return signalled([]*os.File{d})
}

// waitFences blocks until our own render fences signalled when the driver
// cannot wait for them in the commit, for at most fenceWaitLimit each, or
// until the output stops. Client fences are not waited for here: planes take
// only buffers whose fence signalled (clientFenceReady). A failed wait is not
// fatal: the frame may then show early, as it would on any such driver.
func (o *Output) waitFences(fences ...*os.File) {
	if !o.cpuFences {
		return
	}
	ctx := o.runContext
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, fenceWaitLimit)
	defer cancel()
	for _, f := range fences {
		if f == nil {
			continue
		}
		if err := syncfile.Wait(ctx, f); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			o.log.Debug().Err(err).Str("connector", o.conn.name).Msg("wait for frame fence")
		}
	}
}
