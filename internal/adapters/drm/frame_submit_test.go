package drm

import (
	"context"
	"errors"
	"os"
	"testing"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// A fullscreen dmabuf the kernel takes is scanned out: nothing renders.
func TestSubmitFrameScanout(t *testing.T) {
	o, k, commits := testOutput(t)
	o.cursor = nil
	o.primary.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(88, nil).Once()
	s, c := fullscreenScene()
	r := portsmocks.NewMockRenderer(t)
	direct, err := o.submitFrame(context.Background(), r, s, c, map[ports.WindowID]uint64{}, nil, nil)
	if err != nil || !direct {
		t.Fatalf("direct %v err %v", direct, err)
	}
	if v, _ := (*commits)[len(*commits)-1].req.value(tPrimary, pFB); v != 88 {
		t.Fatalf("primary fb %d", v)
	}
	if o.queued != 9 {
		t.Fatalf("queued %d", o.queued)
	}
}

// A scanout flip the kernel refuses composes the same frame instead.
func TestSubmitFrameScanoutRefusedComposes(t *testing.T) {
	o, k, commits := testOutput(t, unix.EINVAL)
	o.cursor, o.tearing, o.vrrProp = nil, false, 0
	o.primary.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(88, nil).Once()
	s, c := fullscreenScene()
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(s, c).Return(nil, nil).Once()
	direct, err := o.submitFrame(context.Background(), r, s, c, map[ports.WindowID]uint64{}, nil, nil)
	if err != nil || direct {
		t.Fatalf("direct %v err %v", direct, err)
	}
	if v, _ := (*commits)[len(*commits)-1].req.value(tPrimary, pFB); v != 70 || o.back != 1 {
		t.Fatalf("primary fb %d back %d", v, o.back)
	}
	if o.reason != "flip_refused" {
		t.Fatalf("reason %q", o.reason)
	}
}

// An overlay the kernel refuses in its test commit is composed too.
func TestSubmitFrameOverlayRefusedComposesAll(t *testing.T) {
	o, k, commits := overlayOutput(t, unix.EINVAL)
	o.cursor = nil
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(88, nil).Once()
	o.overlay.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	s, c := overlayScene()
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	var drawn []int
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, _ map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
		drawn = append(drawn, len(s.Windows))
		return nil, nil
	}).Twice()
	direct, err := o.submitFrame(context.Background(), r, s, c, map[ports.WindowID]uint64{}, nil, nil)
	if err != nil || direct {
		t.Fatalf("direct %v err %v", direct, err)
	}
	// First without the overlay window, then the whole scene.
	if len(drawn) != 2 || drawn[0] != 1 || drawn[1] != 2 {
		t.Fatalf("rendered windows %v", drawn)
	}
	if v, ok := (*commits)[len(*commits)-1].req.value(tOverlay, pFB); ok && v != 0 {
		t.Fatalf("overlay committed: fb %d", v)
	}
}

// An overlay commit the kernel refuses after its test passed drops the
// overlay: the caller asks for a new, fully composed frame.
func TestSubmitFrameOverlayCommitRefusedDropsOverlay(t *testing.T) {
	o, k, _ := overlayOutput(t, nil, unix.EINVAL)
	o.cursor, o.vrrProp = nil, 0
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(88, nil).Once()
	o.overlay.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	s, c := overlayScene()
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
	if _, err := o.submitFrame(context.Background(), r, s, c, map[ports.WindowID]uint64{}, nil, nil); !errors.Is(err, errOverlayDropped) {
		t.Fatalf("err %v", err)
	}
	if o.back != 0 || o.clientFBs[9].overlayFailed != "cursor_conflict" {
		t.Fatalf("back %d reason %q", o.back, o.clientFBs[9].overlayFailed)
	}
}

// A render failure stops the output and is not a commit error.
func TestSubmitFrameRenderError(t *testing.T) {
	o, _, commits := testOutput(t)
	boom := errors.New("boom")
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, boom).Once()
	_, err := o.submitFrame(context.Background(), r, ports.Scene{Seq: 1}, nil, map[ports.WindowID]uint64{}, nil, nil)
	var fatal renderError
	if !errors.As(err, &fatal) || !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	if len(*commits) != 0 || o.back != 0 {
		t.Fatalf("committed %d back %d", len(*commits), o.back)
	}
}

func fullscreenScene() (ports.Scene, map[ports.WindowID]ports.SurfaceContent) {
	s := ports.Scene{Seq: 1, Scale: 1, OutputWidth: 200, OutputHeight: 100, Windows: []ports.SceneWindow{
		{ID: 1, Rect: ports.Rect{W: 200, H: 100}, Fullscreen: true},
	}}
	return s, map[ports.WindowID]ports.SurfaceContent{
		1: {ID: 1, Width: 200, Height: 100, LogicalW: 200, LogicalH: 100, Opaque: true, DMABuf: &ports.DMABuf{ID: 9, Width: 200, Height: 100, Format: fourccXRGB}},
	}
}
