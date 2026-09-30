package capture

import (
	"context"
	"errors"
	"image"
	"math"
	"os"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

// writeRequest is a valid 2x2 request over a 16-byte file.
func writeRequest(t *testing.T) ports.CaptureRequest {
	t.Helper()
	return pipelineRequest(t, 7)
}

// runWriteFrame calls writeFrame with a buffered reply channel and returns its
// single reply.
func runWriteFrame(t *testing.T, req ports.CaptureRequest, frame ports.CaptureFrame) ports.CaptureDone {
	t.Helper()
	replies := make(chan ports.CaptureDone, 1)
	writeFrame(context.Background(), req, frame, time.Unix(5, 6), replies)
	done := awaitCapture(t, replies)
	if done.ID != req.ID {
		t.Fatalf("reply for %d, want %d", done.ID, req.ID)
	}
	if len(replies) != 0 {
		t.Fatal("more than one reply")
	}
	return done
}

func requireClosed(t *testing.T, f *os.File) {
	t.Helper()
	if f == nil {
		return
	}
	if _, err := f.Stat(); err == nil {
		t.Fatal("destination file left open")
	}
}

func TestWriteFrameRejectsInvalidDestination(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, q *ports.CaptureRequest)
	}{
		{"nil file", func(_ *testing.T, q *ports.CaptureRequest) { q.Dst.File = nil }},
		{"zero width", func(_ *testing.T, q *ports.CaptureRequest) { q.Width = 0 }},
		{"negative width", func(_ *testing.T, q *ports.CaptureRequest) { q.Width = -2 }},
		{"zero height", func(_ *testing.T, q *ports.CaptureRequest) { q.Height = 0 }},
		{"negative height", func(_ *testing.T, q *ports.CaptureRequest) { q.Height = -2 }},
		{"stride below row", func(_ *testing.T, q *ports.CaptureRequest) { q.Stride = 4 }},
		{"region width mismatch", func(_ *testing.T, q *ports.CaptureRequest) { q.Region = image.Rect(0, 0, 3, 2) }},
		{"region height mismatch", func(_ *testing.T, q *ports.CaptureRequest) { q.Region = image.Rect(0, 0, 2, 1) }},
		{"negative offset", func(_ *testing.T, q *ports.CaptureRequest) { q.Dst.Offset = -4 }},
		{"invalid format", func(_ *testing.T, q *ports.CaptureRequest) { q.Format = 0xdeadbeef }},
		{"width overflows bound", func(_ *testing.T, q *ports.CaptureRequest) {
			q.Width, q.Region = math.MaxInt32, image.Rect(0, 0, math.MaxInt32, 2)
			q.Stride = q.Width * 4
		}},
		{"huge stride overflowing h-1", func(_ *testing.T, q *ports.CaptureRequest) {
			q.Height, q.Region = 3, image.Rect(0, 0, 2, 3)
			q.Stride = math.MaxInt64 / 2
		}},
		{"max stride", func(_ *testing.T, q *ports.CaptureRequest) { q.Stride = math.MaxInt }},
		{"huge height", func(_ *testing.T, q *ports.CaptureRequest) {
			q.Height, q.Region = math.MaxInt32, image.Rect(0, 0, 2, math.MaxInt32)
		}},
		{"offset past bound", func(_ *testing.T, q *ports.CaptureRequest) { q.Dst.Offset = maxMappedBytes }},
		{"one GiB plus one byte", func(_ *testing.T, q *ports.CaptureRequest) {
			// 1 row of 4 bytes at offset 1GiB-3 ends one byte past the bound.
			q.Width, q.Height, q.Stride, q.Region = 1, 1, 4, image.Rect(0, 0, 1, 1)
			q.Dst.Offset = maxMappedBytes - 3
		}},
		{"truncated file", func(t *testing.T, q *ports.CaptureRequest) {
			if err := q.Dst.File.Truncate(15); err != nil {
				t.Fatal(err)
			}
		}},
		{"offset past end of file", func(_ *testing.T, q *ports.CaptureRequest) { q.Dst.Offset = 4 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := writeRequest(t)
			tc.mutate(t, &req)
			// No Read expectation: any Read call fails the test.
			frame := portsmocks.NewMockCaptureFrame(t)
			done := runWriteFrame(t, req, frame)
			if done.Err == nil {
				t.Fatal("invalid request succeeded")
			}
			requireClosed(t, req.Dst.File)
		})
	}
}

func TestWriteFrameAcceptsExactFit(t *testing.T) {
	req := writeRequest(t)
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Read(req.Region, mock.Anything, req.Stride).RunAndReturn(func(_ image.Rectangle, dst []byte, _ int) error {
		if len(dst) < 8+2*4 {
			t.Errorf("dst holds %d bytes", len(dst))
			return nil
		}
		copy(dst, []byte{1, 2, 3, 255})
		return nil
	}).Once()
	done := runWriteFrame(t, req, frame)
	if done.Err != nil {
		t.Fatal(done.Err)
	}
	if !done.Time.Equal(time.Unix(5, 6)) {
		t.Fatalf("time %v", done.Time)
	}
	requireClosed(t, req.Dst.File)
	// Mockery retains Read arguments, which point at an unmapped SHM slice.
	frame.Calls = nil
}

func TestWriteFramePropagatesReadError(t *testing.T) {
	req := writeRequest(t)
	boom := errors.New("readback failed")
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Read(req.Region, mock.Anything, req.Stride).Return(boom).Once()
	done := runWriteFrame(t, req, frame)
	if !errors.Is(done.Err, boom) {
		t.Fatalf("err %v", done.Err)
	}
	requireClosed(t, req.Dst.File)
	frame.Calls = nil
}

func TestWriteFrameRecoversFromReadPanic(t *testing.T) {
	req := writeRequest(t)
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Read(req.Region, mock.Anything, req.Stride).RunAndReturn(func(image.Rectangle, []byte, int) error {
		panic("renderer bug")
	}).Once()
	done := runWriteFrame(t, req, frame)
	if done.Err == nil {
		t.Fatal("panic not reported")
	}
	requireClosed(t, req.Dst.File)
	frame.Calls = nil
}

// A client can truncate its buffer after the size check while it is mapped;
// touching the mapping then raises SIGBUS, which must fail this capture only.
func TestWriteFrameRecoversFromTruncationFault(t *testing.T) {
	req := writeRequest(t)
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Read(req.Region, mock.Anything, req.Stride).RunAndReturn(func(_ image.Rectangle, dst []byte, _ int) error {
		if err := req.Dst.File.Truncate(0); err != nil {
			return err
		}
		dst[0] = 1 // SIGBUS: the page is past the end of the file
		return nil
	}).Once()
	done := runWriteFrame(t, req, frame)
	if done.Err == nil {
		t.Fatal("fault not reported")
	}
	requireClosed(t, req.Dst.File)
	// The compositor survives and can serve the next capture.
	next := writeRequest(t)
	ok := portsmocks.NewMockCaptureFrame(t)
	ok.EXPECT().Read(next.Region, mock.Anything, next.Stride).Return(nil).Once()
	if done := runWriteFrame(t, next, ok); done.Err != nil {
		t.Fatalf("capture after fault: %v", done.Err)
	}
	frame.Calls = nil
	ok.Calls = nil
}
