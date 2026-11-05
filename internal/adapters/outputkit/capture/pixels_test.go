package capture

import (
	"bytes"
	"context"
	"errors"
	"image"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/domain"
	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
)

var errNoSyncFile = errors.New("capture needs sync file export")

func cpuRequest(id uint64) domain.CaptureRequest {
	return domain.CaptureRequest{ID: id, Region: image.Rect(0, 0, 2, 2), Width: 2, Height: 2, Stride: 8, Format: 1, Mapped: make([]byte, 16)}
}

// A headless pipeline (CPUReadback) answers a capture the renderer cannot
// track on the GPU (no sync-file export) from Renderer.Pixels, as opaque
// BGRA, and holds no renderer lease for it.
func TestPipelineCPUReadbackFallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := portsmocks.NewMockRenderer(t)
		r.EXPECT().BeginCapture().Return(nil, errNoSyncFile).Once()
		img := image.NewRGBA(image.Rect(0, 0, 2, 2))
		for i := 0; i < len(img.Pix); i += 4 {
			copy(img.Pix[i:], []byte{0x12, 0x34, 0x56, 0})
		}
		r.EXPECT().Pixels().Return(img).Once()
		// No EndCapture expectation: the mock fails if the CPU frame is ended.
		replies := make(chan domain.CaptureDone, 1)
		p := NewPipeline(context.Background(), replies)
		p.CPUReadback = true
		q := cpuRequest(1)
		p.SubmitScoped(domain.SecurityState{}, r, []domain.CaptureRequest{q})
		synctest.Wait()
		done := <-replies
		if done.Err != nil || done.ID != 1 {
			t.Fatalf("result %+v", done)
		}
		if want := bytes.Repeat([]byte{0x56, 0x34, 0x12, 255}, 4); !bytes.Equal(q.Mapped, want) {
			t.Fatalf("pixels % x, want % x", q.Mapped, want)
		}
		p.Close(r)
	})
}

// Without CPUReadback (DRM), the renderer's refusal fails the capture.
func TestPipelineWithoutCPUReadbackFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := portsmocks.NewMockRenderer(t)
		r.EXPECT().BeginCapture().Return(nil, errNoSyncFile).Once()
		replies := make(chan domain.CaptureDone, 1)
		p := NewPipeline(context.Background(), replies)
		p.SubmitScoped(domain.SecurityState{}, r, []domain.CaptureRequest{cpuRequest(1)})
		if done := <-replies; !errors.Is(done.Err, errNoSyncFile) {
			t.Fatalf("result %+v", done)
		}
		p.Close(r)
	})
}

// The fallback keeps the renderer's error when Pixels cannot read either.
func TestPipelineCPUReadbackKeepsError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := portsmocks.NewMockRenderer(t)
		r.EXPECT().BeginCapture().Return(nil, errNoSyncFile).Once()
		r.EXPECT().Pixels().Return(nil).Once()
		replies := make(chan domain.CaptureDone, 1)
		p := NewPipeline(context.Background(), replies)
		p.CPUReadback = true
		p.SubmitScoped(domain.SecurityState{}, r, []domain.CaptureRequest{cpuRequest(1)})
		if done := <-replies; !errors.Is(done.Err, errNoSyncFile) {
			t.Fatalf("result %+v", done)
		}
		p.Close(r)
	})
}
