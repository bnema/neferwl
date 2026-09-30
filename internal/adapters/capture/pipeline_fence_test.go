package capture

import (
	"context"
	"errors"
	"image"
	"os"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

// fencePipe returns a pipe standing in for a sync file: readable is signalled.
func fencePipe(t *testing.T) (read, write *os.File) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = read.Close(); _ = write.Close() })
	return read, write
}

func TestPipelineWaitsForFenceBeforeRead(t *testing.T) {
	r := portsmocks.NewMockRenderer(t)
	frame := portsmocks.NewMockCaptureFrame(t)
	fence, signal := fencePipe(t)
	frame.EXPECT().Done().Return(fence)
	entered := make(chan struct{})
	frame.EXPECT().Read(image.Rect(0, 0, 2, 2), mock.Anything, 8).RunAndReturn(func(_ image.Rectangle, dst []byte, _ int) error {
		close(entered)
		copy(dst, []byte{1, 2, 3, 255})
		return nil
	}).Once()
	r.EXPECT().BeginCapture().Return(frame, nil).Once()
	r.EXPECT().EndCapture(frame).Return().Once()
	replies := make(chan ports.CaptureDone, 1)
	p := NewPipeline(context.Background(), replies)
	q := pipelineRequest(t, 1)
	p.SubmitScoped(ports.SecurityState{}, r, []ports.CaptureRequest{q})
	// Longer than one syncfile poll slice: the worker has polled at least once.
	select {
	case <-entered:
		t.Fatal("Read before the fence signalled")
	case <-replies:
		t.Fatal("reply before the fence signalled")
	case <-time.After(250 * time.Millisecond):
	}
	if _, err := signal.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	done := awaitCapture(t, replies)
	if done.Err != nil || done.ID != q.ID {
		t.Fatalf("result %+v", done)
	}
	select {
	case <-entered:
	default:
		t.Fatal("Read never ran")
	}
	if _, err := q.Dst.File.Stat(); err == nil {
		t.Fatal("descriptor open at completion")
	}
	p.Close(r)
	frame.Calls = nil
}

func TestPipelinePropagatesReadError(t *testing.T) {
	r := portsmocks.NewMockRenderer(t)
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil)
	boom := errors.New("readback failed")
	frame.EXPECT().Read(image.Rect(0, 0, 2, 2), mock.Anything, 8).Return(boom).Once()
	r.EXPECT().BeginCapture().Return(frame, nil).Once()
	r.EXPECT().EndCapture(frame).Return().Once()
	replies := make(chan ports.CaptureDone, 1)
	p := NewPipeline(context.Background(), replies)
	q := pipelineRequest(t, 1)
	p.SubmitScoped(ports.SecurityState{}, r, []ports.CaptureRequest{q})
	done := awaitCapture(t, replies)
	if !errors.Is(done.Err, boom) || done.ID != q.ID {
		t.Fatalf("result %+v", done)
	}
	if _, err := q.Dst.File.Stat(); err == nil {
		t.Fatal("descriptor open at completion")
	}
	// The lease returns even though the read failed.
	p.Close(r)
	frame.Calls = nil
}
