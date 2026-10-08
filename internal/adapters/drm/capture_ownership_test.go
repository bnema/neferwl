package drm

import (
	"context"
	"image"
	"os"
	"testing"

	"github.com/bnema/neferwl/internal/adapters/outputkit/capture"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

func TestCaptureOwnershipTransferredBeforeCommitPanic(t *testing.T) {
	o, _, _ := testOutput(t)
	o.cursor = nil
	k := newMockkms(t)
	o.k = k
	k.EXPECT().commit(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(func(*atomicReq, uint32, uint64) error { panic("commit panic") }).Once()
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil).Maybe()
	frame.EXPECT().Read(mock.Anything, mock.Anything, 8).Return(nil).Maybe()
	r.EXPECT().BeginCapture().Return(frame, nil).Once()
	r.EXPECT().EndCapture(frame).Return().Once()
	f, err := os.CreateTemp(t.TempDir(), "capture")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(16); err != nil {
		t.Fatal(err)
	}
	requests := []ports.CaptureRequest{{ID: 1, Dst: ports.SHMBuffer{File: f}, Region: image.Rect(0, 0, 2, 2), Width: 2, Height: 2, Stride: 8, Format: 1}}
	replies := make(chan ports.CaptureDone, 2)
	pipeline := capture.NewPipeline(context.Background(), replies)
	func() {
		defer func() {
			if p := recover(); p != "commit panic" {
				t.Fatalf("panic %v", p)
			}
		}()
		_, _ = o.submitFrame(context.Background(), r, ports.Scene{Seq: 1}, nil, nil, requests, pipeline)
	}()
	pipeline.Close(r)
	if requests[0].ID != 0 || requests[0].Dst.File != nil {
		t.Fatal("owner retained handed-off request")
	}
	if result := <-replies; result.ID != 1 {
		t.Fatalf("reply %+v", result)
	}
	select {
	case result := <-replies:
		t.Fatalf("duplicate reply %+v", result)
	default:
	}
	if _, err := f.Stat(); err == nil {
		t.Fatal("descriptor leaked")
	}
	frame.Calls = nil
}
