// Package capture writes rendered pixels to a client-owned shm buffer.
package capture

import (
	"fmt"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
)

func Fail(req ports.CaptureRequest, err error, replies chan<- ports.CaptureDone) {
	if req.Dst.File != nil {
		_ = req.Dst.File.Close()
	}
	replies <- ports.CaptureDone{ID: req.ID, Output: req.Output, Err: err, Time: time.Now()}
}

func Write(req ports.CaptureRequest, r ports.Renderer, replies chan<- ports.CaptureDone) {
	var err error
	defer func() { Fail(req, err, replies) }()
	if req.Dst.File == nil || req.Width <= 0 || req.Height <= 0 || req.Stride < req.Width*4 || req.Region.Dx() != req.Width || req.Region.Dy() != req.Height || req.Dst.Offset < 0 {
		err = fmt.Errorf("invalid capture destination")
		return
	}
	size := int64(req.Dst.Offset) + int64(req.Height-1)*int64(req.Stride) + int64(req.Width)*4
	if size <= 0 || size > int64(^uint(0)>>1) || size > 1<<30 {
		err = fmt.Errorf("capture too large")
		return
	}
	st, e := req.Dst.File.Stat()
	if e != nil {
		err = e
		return
	}
	if st.Size() < size {
		err = fmt.Errorf("capture buffer truncated")
		return
	}
	// Map only for the duration of this capture; the renderer copies BGRA rows
	// directly from its readback buffer into the selected shm region.
	data, e := unix.Mmap(int(req.Dst.File.Fd()), 0, int(size), unix.PROT_WRITE, unix.MAP_SHARED)
	if e != nil {
		err = e
		return
	}
	defer unix.Munmap(data)
	err = r.Capture(req.Region, data[req.Dst.Offset:], req.Stride)
}
