package wayland

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// syncobjDevice is the DRM syncobj interface of a render node: explicit
// sync timelines clients import (wp_linux_drm_syncobj_v1).
type syncobjDevice interface {
	// fdToHandle imports a timeline syncobj fd.
	fdToHandle(fd int) (uint32, error)
	// exportSyncFile returns a sync file of a timeline point's fence.
	exportSyncFile(h uint32, point uint64) (*os.File, error)
	// signal signals a timeline point (a release point).
	signal(h uint32, point uint64) error
	// eventfd writes efd once the point has a fence (WAIT_AVAILABLE).
	eventfd(h uint32, point uint64, efd int) error
	destroy(h uint32) error
}

const (
	ioctlSyncobjDestroy        = 0xC00864C0 // DRM_IOWR(0xC0, struct drm_syncobj_destroy)
	ioctlSyncobjHandleToFD     = 0xC01864C1 // DRM_IOWR(0xC1, struct drm_syncobj_handle)
	ioctlSyncobjFDToHandle     = 0xC01864C2 // DRM_IOWR(0xC2, struct drm_syncobj_handle)
	ioctlSyncobjTimelineSignal = 0xC01864CD // DRM_IOWR(0xCD, struct drm_syncobj_timeline_array)
	ioctlSyncobjEventfd        = 0xC01864CF // DRM_IOWR(0xCF, struct drm_syncobj_eventfd)
	ioctlGetCap                = 0xC010640C // DRM_IOWR(0x0C, struct drm_get_cap)

	syncobjExportSyncFile = 1 << 0
	syncobjTimeline       = 1 << 1
	syncobjWaitAvailable  = 1 << 2
	capSyncobjTimeline    = 0x14
)

type syncobjHandle struct {
	handle, flags uint32
	fd            int32
	pad           uint32
	point         uint64
}

type syncobjDestroy struct{ handle, pad uint32 }

type syncobjTimelineArray struct {
	handles, points uint64
	count, flags    uint32
}

type syncobjEventfd struct {
	handle, flags uint32
	point         uint64
	fd            int32
	pad           uint32
}

type drmCap struct{ capability, value uint64 }

// renderNode is a DRM render node opened for syncobj ioctls.
type renderNode struct{ fd int }

// openSyncobj opens a render node that supports timeline syncobjs.
func openSyncobj(path string) (*renderNode, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	c := drmCap{capability: capSyncobjTimeline}
	if ioctl(fd, ioctlGetCap, unsafe.Pointer(&c)) != nil || c.value == 0 {
		unix.Close(fd)
		return nil, fmt.Errorf("%s: no timeline syncobjs", path)
	}
	return &renderNode{fd: fd}, nil
}

func (n *renderNode) close() { unix.Close(n.fd) }

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	for {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
		if errno == unix.EINTR || errno == unix.EAGAIN {
			continue
		}
		if errno != 0 {
			return errno
		}
		return nil
	}
}

func (n *renderNode) fdToHandle(fd int) (uint32, error) {
	a := syncobjHandle{fd: int32(fd)}
	if err := ioctl(n.fd, ioctlSyncobjFDToHandle, unsafe.Pointer(&a)); err != nil {
		return 0, err
	}
	return a.handle, nil
}

func (n *renderNode) exportSyncFile(h uint32, point uint64) (*os.File, error) {
	a := syncobjHandle{handle: h, flags: syncobjExportSyncFile | syncobjTimeline, fd: -1, point: point}
	if err := ioctl(n.fd, ioctlSyncobjHandleToFD, unsafe.Pointer(&a)); err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(a.fd), "acquire-fence"), nil
}

func (n *renderNode) signal(h uint32, point uint64) error {
	handles, points := [1]uint32{h}, [1]uint64{point}
	a := syncobjTimelineArray{handles: uint64(uintptr(unsafe.Pointer(&handles))), points: uint64(uintptr(unsafe.Pointer(&points))), count: 1}
	err := ioctl(n.fd, ioctlSyncobjTimelineSignal, unsafe.Pointer(&a))
	runtime.KeepAlive(&handles)
	runtime.KeepAlive(&points)
	return err
}

func (n *renderNode) eventfd(h uint32, point uint64, efd int) error {
	a := syncobjEventfd{handle: h, flags: syncobjWaitAvailable, point: point, fd: int32(efd)}
	return ioctl(n.fd, ioctlSyncobjEventfd, unsafe.Pointer(&a))
}

func (n *renderNode) destroy(h uint32) error {
	a := syncobjDestroy{handle: h}
	return ioctl(n.fd, ioctlSyncobjDestroy, unsafe.Pointer(&a))
}
