package capture

import (
	"context"
	"errors"
	"image"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/stretchr/testify/mock"
)

// pqRequest is a screencast frame into a lent XB30 dmabuf.
func pqRequest(t *testing.T, id uint64) domain.CaptureRequest {
	q := dmabufRequest(t, id)
	q.DMABuf.Format, q.Format, q.DMABufBorrowed = domain.CastXBGR2101010, domain.CastXBGR2101010, true
	return q
}

// PQ requests are copied from a BeginCapturePQ frame, the others read from
// a BeginCapture frame: one frame per readback in a mixed batch. A lent
// buffer's files stay open.
func TestPipelineRoutesPQRequests(t *testing.T) {
	r := portsmocks.NewMockRenderer(t)
	sdr, pq := portsmocks.NewMockCaptureFrame(t), portsmocks.NewMockCaptureFrame(t)
	for _, f := range []*portsmocks.MockCaptureFrame{sdr, pq} {
		f.EXPECT().Done().Return(nil)
		r.EXPECT().EndCapture(f).Return().Once()
	}
	sdr.EXPECT().Read(image.Rect(0, 0, 2, 2), mock.Anything, 8).Return(nil).Once()
	pq.EXPECT().CopyTo(image.Rect(1, 1, 3, 3), mock.Anything).Return(nil, nil).Twice()
	r.EXPECT().BeginCapture().Return(sdr, nil).Once()
	r.EXPECT().BeginCapturePQ().Return(pq, nil).Once()
	replies := make(chan domain.CaptureDone, 3)
	p := NewPipeline(context.Background(), replies)
	lent := pqRequest(t, 1)
	p.SubmitScoped(domain.SecurityState{}, r, []domain.CaptureRequest{lent, pipelineRequest(t, 2), pqRequest(t, 3)})
	seen := map[uint64]bool{}
	for range 3 {
		done := awaitCapture(t, replies)
		if done.Err != nil {
			t.Fatalf("result %+v", done)
		}
		seen[done.ID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("answered %v", seen)
	}
	if _, err := lent.DMABuf.Planes[0].File.Stat(); err != nil {
		t.Fatalf("lent plane closed: %v", err)
	}
	// Read arguments point into unmapped destinations: clear them before
	// Close, whose EndCapture matching compares the two frames.
	sdr.Calls, pq.Calls = nil, nil
	p.Close(r)
}

// Without an HDR frame a PQ request fails transiently: the cast retries
// instead of ending. SDR requests of the same batch are served.
func TestPipelinePQWithoutHDRFrameIsTransient(t *testing.T) {
	r := portsmocks.NewMockRenderer(t)
	sdr := portsmocks.NewMockCaptureFrame(t)
	sdr.EXPECT().Done().Return(nil)
	sdr.EXPECT().Read(image.Rect(0, 0, 2, 2), mock.Anything, 8).Return(nil).Once()
	r.EXPECT().BeginCapture().Return(sdr, nil).Once()
	r.EXPECT().EndCapture(sdr).Return().Once()
	r.EXPECT().BeginCapturePQ().Return(nil, errors.New("no HDR frame rendered")).Once()
	replies := make(chan domain.CaptureDone, 2)
	p := NewPipeline(context.Background(), replies)
	p.SubmitScoped(domain.SecurityState{}, r, []domain.CaptureRequest{pipelineRequest(t, 1), pqRequest(t, 2)})
	for range 2 {
		done := awaitCapture(t, replies)
		switch done.ID {
		case 1:
			if done.Err != nil {
				t.Fatalf("SDR result %+v", done)
			}
		case 2:
			if !errors.Is(done.Err, ports.ErrCaptureTransient) {
				t.Fatalf("PQ result %+v", done)
			}
		}
	}
	p.Close(r)
	sdr.Calls = nil
}

// No PQ words reach CPU memory: SHM destinations take wl_shm formats only,
// and an XB30 SHM request is an ordinary (refused) SDR request.
func TestDestSizeRefusesXB30(t *testing.T) {
	q := pipelineRequest(t, 1)
	q.Format = domain.CastXBGR2101010
	if _, err := destSize(q); err == nil {
		t.Fatal("XB30 SHM destination accepted")
	}
	if isPQ(q) {
		t.Fatal("SHM request routed to the PQ readback")
	}
	q.Format = 0x34325258 // XR24 as a DRM code: not a wl_shm format here
	if _, err := destSize(q); err == nil {
		t.Fatal("unknown format accepted")
	}
}
