package headless

import (
	"context"
	"image"
	"os"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/adapters/capture"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

func TestScreenshotReadbackFailureDoesNotWriteBlackPNG(t *testing.T) {
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
	scenes := make(chan ports.Scene, 1)
	scenes <- ports.Scene{Seq: 1, Scale: 1}
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempted := make(chan struct{})
	// Synchronize on the actual readback attempt without assuming timing.
	r.EXPECT().Pixels().Run(func() { close(attempted) }).Return(nil).Once()
	r.EXPECT().Close().Return().Once()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Width: 2, Height: 2, ScreenshotDir: dir, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }}, scenes, nil, nil, nil)
	}()
	select {
	case <-attempted:
	case <-time.After(time.Second):
		t.Fatal("no readback attempt")
	}
	select {
	case err := <-done:
		t.Fatalf("readback unavailable stopped output: %v", err)
	default:
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("failed screenshot wrote files: %v (%v)", files, err)
	}
}

func TestCaptureBatchBoundedAndRecycledWhileIdle(t *testing.T) {
	r, _ := recordingRenderer(t, nil)
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil)
	frame.EXPECT().Read(mock.Anything, mock.Anything, 8).Return(nil).Times(capture.MaxRequests)
	recycled := make(chan struct{})
	r.EXPECT().BeginCapture().Return(frame, nil).Once()
	r.EXPECT().EndCapture(frame).Run(func(ports.CaptureFrame) { close(recycled) }).Return().Once()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	incoming := make(chan ports.CaptureRequest)
	scenes := make(chan ports.Scene)
	replies := make(chan ports.CaptureDone, capture.MaxRequests+1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Width: 2, Height: 2, NewRenderer: func(int, int) (ports.Renderer, error) { return r, nil }, Captured: replies}, scenes, nil, nil, incoming)
	}()
	for i := range capture.MaxRequests + 1 {
		f, err := os.CreateTemp(t.TempDir(), "capture")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		if err := f.Truncate(16); err != nil {
			t.Fatal(err)
		}
		select {
		case incoming <- ports.CaptureRequest{ID: uint64(i), Region: image.Rect(0, 0, 2, 2), Width: 2, Height: 2, Stride: 8, Format: 1, Dst: ports.SHMBuffer{File: f}}:
		case <-time.After(time.Second):
			t.Fatal("capture admission blocked")
		}
	}
	select {
	case rejected := <-replies:
		if rejected.ID != capture.MaxRequests || rejected.Err == nil {
			t.Fatalf("batch overflow %+v", rejected)
		}
	case <-time.After(time.Second):
		t.Fatal("batch overflow not answered")
	}
	scenes <- ports.Scene{Seq: 1, Scale: 1}
	for range capture.MaxRequests {
		select {
		case result := <-replies:
			if result.Err != nil {
				t.Fatalf("capture %+v", result)
			}
		case <-time.After(time.Second):
			t.Fatal("accepted capture not answered")
		}
	}
	select {
	case <-recycled:
	case <-time.After(time.Second):
		t.Fatal("idle output did not return readback lease")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	frame.Calls = nil
}
