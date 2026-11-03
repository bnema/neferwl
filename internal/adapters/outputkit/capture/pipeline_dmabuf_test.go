package capture

import (
	"context"
	"errors"
	"image"
	"os"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/stretchr/testify/mock"
)

func dmabufRequest(t *testing.T, id uint64) domain.CaptureRequest {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "plane")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	buf := &domain.DMABuf{ID: id, Width: 2, Height: 2, Format: 'X' | 'R'<<8 | '2'<<16 | '4'<<24, Planes: []domain.DMABufPlane{{File: f, Stride: 8}}}
	return domain.CaptureRequest{ID: id, Region: image.Rect(1, 1, 3, 3), Width: 2, Height: 2, DMABuf: buf}
}

// A dmabuf request is copied on the owner (CopyTo, never Read) and answered
// only once the copy's sync file signalled; its plane files are closed.
//
//testpolicy:realtime a real pipe stands in for the copy's sync file
func TestPipelineDMABufWaitsForCopy(t *testing.T) {
	r := portsmocks.NewMockRenderer(t)
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil)
	fence, signal := fencePipe(t)
	q := dmabufRequest(t, 1)
	frame.EXPECT().CopyTo(image.Rect(1, 1, 3, 3), *q.DMABuf).Return(fence, nil).Once()
	r.EXPECT().BeginCapture().Return(frame, nil).Once()
	r.EXPECT().EndCapture(frame).Return().Once()
	replies := make(chan domain.CaptureDone, 1)
	p := NewPipeline(context.Background(), replies)
	p.SubmitScoped(domain.SecurityState{}, r, []domain.CaptureRequest{q})
	select {
	case <-replies:
		t.Fatal("reply before the copy signalled")
	case <-time.After(250 * time.Millisecond):
	}
	if _, err := signal.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if done := awaitCapture(t, replies); done.Err != nil || done.ID != 1 {
		t.Fatalf("result %+v", done)
	}
	if _, err := q.DMABuf.Planes[0].File.Stat(); err == nil {
		t.Fatal("plane open at completion")
	}
	p.Close(r)
}

// A CopyTo error fails that request only; the SHM one of the batch is read.
func TestPipelineDMABufCopyError(t *testing.T) {
	r := portsmocks.NewMockRenderer(t)
	frame := portsmocks.NewMockCaptureFrame(t)
	frame.EXPECT().Done().Return(nil)
	boom := errors.New("import failed")
	frame.EXPECT().CopyTo(mock.Anything, mock.Anything).Return(nil, boom).Once()
	frame.EXPECT().Read(image.Rect(0, 0, 2, 2), mock.Anything, 8).Return(nil).Once()
	r.EXPECT().BeginCapture().Return(frame, nil).Once()
	r.EXPECT().EndCapture(frame).Return().Once()
	replies := make(chan domain.CaptureDone, 2)
	p := NewPipeline(context.Background(), replies)
	p.SubmitScoped(domain.SecurityState{}, r, []domain.CaptureRequest{dmabufRequest(t, 1), pipelineRequest(t, 2)})
	got := map[uint64]error{}
	for range 2 {
		d := awaitCapture(t, replies)
		got[d.ID] = d.Err
	}
	if !errors.Is(got[1], boom) || got[2] != nil {
		t.Fatalf("results %v", got)
	}
	p.Close(r)
	frame.Calls = nil
}

// A gated batch copies nothing into client buffers until it is released,
// and nothing at all when the indicator frame failed.
func TestPipelineDMABufGated(t *testing.T) {
	for _, verdict := range []error{nil, errors.New("flip failed")} {
		r := portsmocks.NewMockRenderer(t)
		frame := portsmocks.NewMockCaptureFrame(t)
		frame.EXPECT().Done().Return(nil)
		r.EXPECT().BeginCapture().Return(frame, nil).Once()
		r.EXPECT().EndCapture(frame).Return().Once()
		replies := make(chan domain.CaptureDone, 1)
		p := NewPipeline(context.Background(), replies)
		p.BeginGate()
		p.SubmitScoped(domain.SecurityState{}, r, []domain.CaptureRequest{dmabufRequest(t, 1)})
		if verdict == nil {
			frame.EXPECT().CopyTo(mock.Anything, mock.Anything).Return(nil, nil).Once()
		}
		p.EndGate(GateVerdict(verdict))
		done := awaitCapture(t, replies)
		if (done.Err == nil) != (verdict == nil) {
			t.Fatalf("verdict %v: result %+v", verdict, done)
		}
		b := <-p.Completed()
		p.Recycle(b, r)
		p.Close(r)
	}
}
