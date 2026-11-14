package capture

import (
	"context"
	"errors"
	"fmt"
	"image"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
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
			if pq {
				a, b = pqRequest(t, 1), pqRequest(t, 2)
				r.EXPECT().BeginCapturePQ().Return(plain, nil).Once()
				plain.EXPECT().CopyTo(image.Rect(1, 1, 3, 3), mock.Anything).Return(nil, nil).Once()
				with.EXPECT().CopyTo(image.Rect(1, 1, 3, 3), mock.Anything).Return(nil, nil).Once()
			} else {
				r.EXPECT().BeginCapture().Return(plain, nil).Once()
				plain.EXPECT().Read(image.Rect(0, 0, 2, 2), mock.Anything, 8).Return(nil).Once()
				with.EXPECT().Read(image.Rect(0, 0, 2, 2), mock.Anything, 8).Return(nil).Once()
			}
			b.Cursor = true
			r.EXPECT().BeginCaptureCursor(cursor, pq).Return(with, nil).Once()
			replies := make(chan domain.CaptureDone, 2)
			p := NewPipeline(context.Background(), replies)
			p.Cursor = cursor
			p.SubmitScoped(domain.SecurityState{}, r, []domain.CaptureRequest{a, b})
			for range 2 {
				if done := awaitCapture(t, replies); done.Err != nil {
					t.Fatal(done.Err)
				}
			}
			// Read/CopyTo arguments point into unmapped destinations: clear
			// them before Close, whose EndCapture matching compares frames.
			plain.Calls, with.Calls = nil, nil
			p.Close(r)
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

func TestPipelineCursorErrorClassification(t *testing.T) {
	for _, cause := range []error{errors.New("invalid capture cursor image"), ports.ErrCaptureBusy} {
		t.Run(cause.Error(), func(t *testing.T) {
			r := portsmocks.NewMockRenderer(t)
			cursor := &domain.CaptureCursor{Image: domain.CursorImage{W: 1, H: 1}}
			q := pipelineRequest(t, 1)
			q.Cursor = true
			r.EXPECT().BeginCaptureCursor(cursor, false).Return(nil, cause).Once()
			replies := make(chan domain.CaptureDone, 1)
			p := NewPipeline(context.Background(), replies)
			p.Cursor = cursor
			p.SubmitScoped(domain.SecurityState{}, r, []domain.CaptureRequest{q})
			done := awaitCapture(t, replies)
			if !errors.Is(done.Err, cause) {
				t.Fatalf("lost original error: %v", done.Err)
			}
			if got, want := errors.Is(done.Err, ports.ErrCaptureTransient), errors.Is(cause, ports.ErrCaptureTransient); got != want {
				t.Fatalf("transient=%v want %v: %v", got, want, done.Err)
			}
			p.Close(r)
		})
	}
}

func TestPipelineClassFairness(t *testing.T) {
	for _, count := range []int{3, 4} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := portsmocks.NewMockRenderer(t)
				cursor := &domain.CaptureCursor{Image: domain.CursorImage{W: 1, H: 1}}
				frames := make([]*portsmocks.MockCaptureFrame, count)
				requests := make([]domain.CaptureRequest, count)
				for k := range count {
					f := portsmocks.NewMockCaptureFrame(t)
					frames[k] = f
					f.EXPECT().Done().Return(nil)
					r.EXPECT().EndCapture(f).Return()
					if k%2 == 0 {
						requests[k] = pipelineRequest(t, uint64(k+1))
						f.EXPECT().Read(mock.Anything, mock.Anything, mock.Anything).Return(nil)
					} else {
						requests[k] = pqRequest(t, uint64(k+1))
						f.EXPECT().CopyTo(mock.Anything, mock.Anything).Return(nil, nil)
					}
					requests[k].Cursor = k >= 2
					if k >= 2 {
						r.EXPECT().BeginCaptureCursor(cursor, k%2 != 0).Return(f, nil)
					} else if k == 1 {
						r.EXPECT().BeginCapturePQ().Return(f, nil)
					} else {
						r.EXPECT().BeginCapture().Return(f, nil)
					}
				}
				replies := make(chan domain.CaptureDone, MaxRequests)
				p := NewPipeline(context.Background(), replies)
				p.Cursor = cursor
				seen := make(map[uint64]bool)
				for range 4 {
					for k := range count {
						if k%2 == 0 {
							requests[k] = pipelineRequest(t, uint64(k+1))
						} else {
							requests[k] = pqRequest(t, uint64(k+1))
						}
						requests[k].Cursor = k >= 2
					}
					// Hold both admitted batches until all classes have attempted admission,
					// so worker scheduling cannot mask saturation and the old starvation.
					p.BeginGate()
					p.SubmitScoped(domain.SecurityState{}, r, requests)
					p.EndGate(nil)
					synctest.Wait()
					for _, f := range frames {
						f.Calls = nil
					}
					for range count {
						select {
						case done := <-replies:
							if done.Err == nil {
								seen[done.ID] = true
							} else if !errors.Is(done.Err, ports.ErrCaptureTransient) {
								t.Fatal(done.Err)
							}
						default:
							t.Fatal("missing class reply")
						}
					}
					p.reclaim(r)
				}
				if len(seen) != count {
					t.Fatalf("served classes within four frames: %v; want %d", seen, count)
				}
				// Generated mocks retain Read/CopyTo arguments into unmapped destinations.
				for _, f := range frames {
					f.Calls = nil
				}
				p.Close(r)
			})
		})
	}
}
