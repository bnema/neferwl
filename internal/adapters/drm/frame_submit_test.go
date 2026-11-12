package drm

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// A fullscreen dmabuf the kernel takes is scanned out: nothing renders.
func TestSubmitFrameScanout(t *testing.T) {
	o, k, commits := testOutput(t)
	o.cursor = nil
	o.primary.formats = []domain.DMABufFormat{{Format: fourccXRGB}}
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(88, nil).Once()
	s, c := fullscreenScene()
	r := portsmocks.NewMockRenderer(t)
	direct, err := o.submitFrame(context.Background(), r, s, c, map[domain.WindowID]uint64{}, nil, nil)
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
	o.primary.formats = []domain.DMABufFormat{{Format: fourccXRGB}}
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(88, nil).Once()
	s, c := fullscreenScene()
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(s, c).Return(nil, nil).Once()
	direct, err := o.submitFrame(context.Background(), r, s, c, map[domain.WindowID]uint64{}, nil, nil)
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
	o.overlay.formats = []domain.DMABufFormat{{Format: fourccXRGB}}
	s, c := overlayScene()
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	var drawn []int
	r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s domain.Scene, _ map[domain.WindowID]domain.SurfaceContent) (*os.File, error) {
		drawn = append(drawn, len(s.Windows))
		return nil, nil
	}).Twice()
	direct, err := o.submitFrame(context.Background(), r, s, c, map[domain.WindowID]uint64{}, nil, nil)
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
	o.overlay.formats = []domain.DMABufFormat{{Format: fourccXRGB}}
	s, c := overlayScene()
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
	if _, err := o.submitFrame(context.Background(), r, s, c, map[domain.WindowID]uint64{}, nil, nil); !errors.Is(err, errOverlayDropped) {
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
	_, err := o.submitFrame(context.Background(), r, domain.Scene{Seq: 1}, nil, map[domain.WindowID]uint64{}, nil, nil)
	var fatal renderError
	if !errors.As(err, &fatal) || !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	if len(*commits) != 0 || o.back != 0 {
		t.Fatalf("committed %d back %d", len(*commits), o.back)
	}
}

func fullscreenScene() (domain.Scene, map[domain.WindowID]domain.SurfaceContent) {
	s := domain.Scene{Seq: 1, Scale: 1, OutputWidth: 200, OutputHeight: 100, Windows: []domain.SceneWindow{
		{ID: 1, Rect: domain.Rect{W: 200, H: 100}, Fullscreen: true},
	}}
	return s, map[domain.WindowID]domain.SurfaceContent{
		1: {ID: 1, Width: 200, Height: 100, LogicalW: 200, LogicalH: 100, Opaque: true, DMABuf: &domain.DMABuf{ID: 9, Width: 200, Height: 100, Format: fourccXRGB}},
	}
}

func TestSubmitFrameTransfersRenderFence(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			var errs []error
			if fail {
				errs = []error{unix.EIO}
			}
			o, _, _ := testOutput(t, errs...)
			o.cursor, o.vrrProp = nil, 0
			fence, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = writer.Close() }()
			r := portsmocks.NewMockRenderer(t)
			r.EXPECT().UseTarget(0).Return().Once()
			r.EXPECT().Render(mock.Anything, mock.Anything).Return(fence, nil).Once()
			_, err = o.submitFrame(context.Background(), r, domain.Scene{Seq: 1}, nil, map[domain.WindowID]uint64{}, nil, nil)
			if fail {
				if err == nil {
					t.Fatal("commit succeeded")
				}
				if len(o.readFences) != 1 || o.readFences[0] == fence {
					t.Fatal("failed render lacks independent read hold")
				}
				if _, err := o.readFences[0].Stat(); err != nil {
					t.Fatal(err)
				}
				o.dropRead()
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if len(o.frame.pendingFrame.fences) != 1 || o.frame.pendingFrame.fences[0] != fence {
					t.Fatal("render fence not transferred")
				}
				if _, err := fence.Stat(); err != nil {
					t.Fatal("closed before flip", err)
				}
				o.frame.flip(o.frame.pendingSerial<<userKindBits | userFrame)
			}
			if err := fence.Close(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("fence not already closed: %v", err)
			}
		})
	}
}

func TestRenderFenceTransferRetries(t *testing.T) {
	for _, cursor := range []bool{false, true} {
		t.Run(map[bool]string{false: "vrr", true: "cursor"}[cursor], func(t *testing.T) {
			errs := []error{unix.EINVAL}
			if cursor {
				errs = append(errs, unix.EINVAL)
			}
			o, _, commits := testOutput(t, errs...)
			if cursor {
				o.cursor.image = true
				o.cursor.Move(12, 34)
				o.vrrProp = 0
			} else {
				o.cursor = nil
			}
			fence, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = w.Close() }()
			if err := o.commitWith(70, fence, false, !cursor, pendingFrame{takesFence: true}, overlayWin{}); err != nil {
				t.Fatal(err)
			}
			if len(*commits) < 2 {
				t.Fatal("no retry")
			}
			if _, err := fence.Stat(); err != nil {
				t.Fatal("closed during retry", err)
			}
			if o.frame.pendingFrame.fences[0] != fence {
				t.Fatal("fence duplicated")
			}
			o.frame.flip(o.frame.pendingSerial<<userKindBits | userFrame)
			if err := fence.Close(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("fence not closed at flip: %v", err)
			}
		})
	}
}
