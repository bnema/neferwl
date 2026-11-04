package capture

import (
	"context"
	"image"
	"testing"

	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/stretchr/testify/mock"
)

func TestPipelineCursorMixedRequests(t *testing.T) {
	for _, pq := range []bool{false, true} {
		t.Run(map[bool]string{false: "SDR", true: "PQ"}[pq], func(t *testing.T) {
			r := portsmocks.NewMockRenderer(t)
			plain, with := portsmocks.NewMockCaptureFrame(t), portsmocks.NewMockCaptureFrame(t)
			for _, f := range []*portsmocks.MockCaptureFrame{plain, with} {
				f.EXPECT().Done().Return(nil)
				r.EXPECT().EndCapture(f).Return().Once()
			}
			cursor := &domain.CaptureCursor{X: 4, Y: 5, Image: domain.CursorImage{W: 1, H: 1, Pixels: []byte{255, 255, 255, 255}}}
			a, b := pipelineRequest(t, 1), pipelineRequest(t, 2)
			b.Cursor = true
			if pq {
				a.Format = domain.CastXBGR2101010
				b.Format = a.Format
				r.EXPECT().BeginCapturePQ().Return(plain, nil).Once()
			} else {
				r.EXPECT().BeginCapture().Return(plain, nil).Once()
			}
			r.EXPECT().BeginCaptureCursor(cursor, pq).Return(with, nil).Once()
			plain.EXPECT().Read(image.Rect(0, 0, 2, 2), mock.Anything, 8).Return(nil).Once()
			with.EXPECT().Read(image.Rect(0, 0, 2, 2), mock.Anything, 8).Return(nil).Once()
			replies := make(chan domain.CaptureDone, 2)
			p := NewPipeline(context.Background(), replies)
			p.Cursor = cursor
			p.SubmitScoped(domain.SecurityState{}, r, []domain.CaptureRequest{a, b})
			for range 2 {
				if done := awaitCapture(t, replies); done.Err != nil {
					t.Fatal(done.Err)
				}
			}
			p.Close(r)
			plain.Calls, with.Calls = nil, nil
		})
	}
}

func TestPipelineCursorDMABuf(t *testing.T) {
	r := portsmocks.NewMockRenderer(t)
	f := portsmocks.NewMockCaptureFrame(t)
	c := &domain.CaptureCursor{Image: domain.CursorImage{W: 1, H: 1, Pixels: []byte{0, 0, 255, 255}}}
	q := dmabufRequest(t, 1)
	q.Cursor = true
	r.EXPECT().BeginCaptureCursor(c, false).Return(f, nil).Once()
	f.EXPECT().Done().Return(nil)
	f.EXPECT().CopyTo(q.Region, *q.DMABuf).Return(nil, nil).Once()
	r.EXPECT().EndCapture(f).Return().Once()
	replies := make(chan domain.CaptureDone, 1)
	p := NewPipeline(context.Background(), replies)
	p.Cursor = c
	p.SubmitScoped(domain.SecurityState{}, r, []domain.CaptureRequest{q})
	if done := awaitCapture(t, replies); done.Err != nil {
		t.Fatal(done.Err)
	}
	p.Close(r)
}
