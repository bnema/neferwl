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

func pipelineRequest(t *testing.T, id uint64) ports.CaptureRequest {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := f.Truncate(16); err != nil {
		t.Fatal(err)
	}
	return ports.CaptureRequest{ID: id, Region: image.Rect(0, 0, 2, 2), Width: 2, Height: 2, Stride: 8, Format: 1, Dst: ports.SHMBuffer{File: f}}
}

func awaitCapture(t *testing.T, replies <-chan ports.CaptureDone) ports.CaptureDone {
	t.Helper()
	select {
	case done := <-replies:
		return done
	case <-time.After(2 * time.Second):
		t.Fatal("no capture reply")
		return ports.CaptureDone{}
	}
}

func TestPipelineCopiesOffOwnerAndRecycles(t *testing.T) {
	r := portsmocks.NewMockRenderer(t)
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil)
	entered, release := make(chan struct{}), make(chan struct{})
	frame.EXPECT().Read(image.Rect(0, 0, 2, 2), mock.Anything, 8).RunAndReturn(func(_ image.Rectangle, dst []byte, _ int) error {
		close(entered)
		<-release
		copy(dst, []byte{1, 2, 3, 255})
		return nil
	}).Once()
	r.EXPECT().BeginCapture().Return(frame, nil).Once()
	r.EXPECT().EndCapture(frame).Return().Once()
	replies := make(chan ports.CaptureDone, 1)
	p := NewPipeline(context.Background(), replies)
	q := pipelineRequest(t, 1)
	p.Submit(r, []ports.CaptureRequest{q})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("copy worker did not run")
	}
	// Submit returned while the worker is still inside Read.
	select {
	case <-replies:
		t.Fatal("reply before copy completed")
	default:
	}
	close(release)
	done := awaitCapture(t, replies)
	if done.Err != nil || done.ID != q.ID {
		t.Fatalf("result %+v", done)
	}
	if _, err := q.Dst.File.Stat(); err == nil {
		t.Fatal("descriptor open at completion")
	}
	p.Close(r)
	// Mockery retains Read arguments, which point at an unmapped SHM slice.
	frame.Calls = nil
}

func TestPipelineSaturationAndShutdownWithoutGPUCompletion(t *testing.T) {
	r := portsmocks.NewMockRenderer(t)
	replies := make(chan ports.CaptureDone, 4)
	p := NewPipeline(context.Background(), replies)
	for id := range uint64(2) {
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = read.Close(); _ = write.Close() })
		frame := portsmocks.NewMockCaptureFrame(t)
		frame.EXPECT().Done().Return(read).Maybe()
		r.EXPECT().BeginCapture().Return(frame, nil).Once()
		r.EXPECT().EndCapture(frame).Return().Once()
		p.Submit(r, []ports.CaptureRequest{pipelineRequest(t, id)})
	}
	q := pipelineRequest(t, 3)
	p.Submit(r, []ports.CaptureRequest{q})
	if done := awaitCapture(t, replies); done.ID != 3 || done.Err == nil {
		t.Fatalf("saturation result %+v", done)
	}
	closed := make(chan struct{})
	go func() { p.Close(r); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("shutdown waited for GPU fence")
	}
	for range 2 {
		if done := awaitCapture(t, replies); done.Err == nil {
			t.Fatalf("cancelled result %+v", done)
		}
	}
}

func TestPipelineBeginFailureClosesRequest(t *testing.T) {
	r := portsmocks.NewMockRenderer(t)
	boom := errors.New("submission failed")
	r.EXPECT().BeginCapture().Return(nil, boom).Once()
	replies := make(chan ports.CaptureDone, 1)
	p := NewPipeline(context.Background(), replies)
	q := pipelineRequest(t, 1)
	p.Submit(r, []ports.CaptureRequest{q})
	if done := awaitCapture(t, replies); !errors.Is(done.Err, boom) {
		t.Fatalf("result %+v", done)
	}
	if _, err := q.Dst.File.Stat(); err == nil {
		t.Fatal("descriptor open")
	}
	p.Close(r)
}

func TestPipelineRejectsOversizedBatch(t *testing.T) {
	r := portsmocks.NewMockRenderer(t)
	replies := make(chan ports.CaptureDone, MaxRequests+1)
	p := NewPipeline(context.Background(), replies)
	requests := make([]ports.CaptureRequest, MaxRequests+1)
	for i := range requests {
		requests[i] = pipelineRequest(t, uint64(i))
	}
	p.Submit(r, requests)
	for range requests {
		if done := awaitCapture(t, replies); done.Err == nil {
			t.Fatal("oversized batch accepted")
		}
	}
	p.Close(r)
}
