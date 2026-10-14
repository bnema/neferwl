package drm

import (
	"context"
	"os"
	"testing"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

// overlayFrames submits the overlay scene once (composed), then with the
// overlay window's next buffers: the frames after the first only flip the
// overlay buffer. It returns the windows each render drew.
func overlayFrames(t *testing.T, o *Output, r *portsmocks.MockRenderer, s ports.Scene, c map[ports.WindowID]ports.SurfaceContent, frames int) {
	t.Helper()
	for i := range frames {
		win := c[2]
		buf := *win.DMABuf
		buf.ID = uint64(9 + i)
		win.DMABuf, win.Seq = &buf, uint64(1+i)
		c[2] = win
		if _, err := o.submitFrame(context.Background(), r, s, c, map[ports.WindowID]uint64{}, nil, nil); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		o.frame.endPending()
	}
}

// When only the overlay window's buffer changes, the image on screen is
// committed again with the new buffer: no render, no probe once KMS took
// the same frame.
func TestSubmitFrameOverlayOnlySkipsRender(t *testing.T) {
	o, k, commits := overlayOutput(t)
	o.cursor, o.vrrProp = nil, 0
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).RunAndReturn(func(b *ports.DMABuf, _ uint32) (uint32, error) {
		return uint32(80 + b.ID), nil
	}).Times(4)
	o.overlay.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	s, c := overlayScene()
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(0).Return().Once()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Once()
	overlayFrames(t, o, r, s, c, 4)
	var probes, frames int
	for _, cm := range *commits {
		if cm.flags&atomicTestOnly != 0 {
			probes++
			continue
		}
		frames++
		if v, _ := cm.req.value(tPrimary, pFB); v != 70 {
			t.Fatalf("frame %d primary fb %d, want the composed image 70", frames, v)
		}
		if _, ok := cm.req.value(tPrimary, pFence); ok {
			t.Fatalf("frame %d waits on a render fence", frames)
		}
	}
	if frames != 4 {
		t.Fatalf("%d frame commits", frames)
	}
	// Each new overlay buffer is probed once; nothing else is.
	if probes != 4 {
		t.Fatalf("%d probes", probes)
	}
	if last := (*commits)[len(*commits)-1]; o.queued != 12 {
		t.Fatalf("queued %d", o.queued)
	} else if v, _ := last.req.value(tOverlay, pFB); v != 92 {
		t.Fatalf("overlay fb %d", v)
	}
	if o.back != 1 {
		t.Fatalf("back %d: the skipped frames drew nothing", o.back)
	}
}

// An uncommitted render that may still read client buffers forces
// composition: the overlay-only flip carries no fence to order it after
// those reads, and its report would let wayland release them.
func TestSubmitFrameOverlayOnlyWaitsForHeldReads(t *testing.T) {
	o, k, _ := overlayOutput(t)
	o.cursor, o.vrrProp = nil, 0
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(88, nil).Maybe()
	o.overlay.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	s, c := overlayScene()
	r := portsmocks.NewMockRenderer(t)
	r.EXPECT().UseTarget(mock.Anything).Return().Twice()
	r.EXPECT().Render(mock.Anything, mock.Anything).Return(nil, nil).Twice()
	overlayFrames(t, o, r, s, c, 1)
	// An unsignalled fence of a render whose commit failed.
	fence, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	defer w.Close()
	o.holdRead(fence)
	defer o.dropRead()
	overlayFrames(t, o, r, s, c, 1)
}

// A frame whose buffer KMS took before is not probed again.
func TestOverlayProbeCached(t *testing.T) {
	o, k, commits := overlayOutput(t)
	o.cursor = nil
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(88, nil).Once()
	o.overlay.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	s, c := overlayScene()
	ov, _ := o.overlayFrame(s, c)
	for range 3 {
		if !o.testOverlay(70, ov) {
			t.Fatal("refused")
		}
	}
	if len(*commits) != 1 {
		t.Fatalf("%d probes, want 1", len(*commits))
	}
	// Another place, primary image or cursor is probed.
	moved := ov
	moved.rect.X++
	o.testOverlay(70, moved)
	o.testOverlay(71, ov)
	if len(*commits) != 3 {
		t.Fatalf("%d probes, want 3", len(*commits))
	}
	// A modeset forgets them.
	o.overlayPassed = [2]overlayProbe{}
	o.testOverlay(70, ov)
	if len(*commits) != 4 {
		t.Fatalf("%d probes after forgetting", len(*commits))
	}
}

// Anything but the overlay window changing composes: another window's
// content, the scene, or an image drawn by another path.
func TestSubmitFrameOverlayOnlyNeedsSameImage(t *testing.T) {
	for name, edit := range map[string]func(o *Output, s *ports.Scene, c map[ports.WindowID]ports.SurfaceContent){
		"other window content": func(_ *Output, _ *ports.Scene, c map[ports.WindowID]ports.SurfaceContent) {
			w := c[1]
			w.Seq++
			c[1] = w
		},
		"scene": func(_ *Output, s *ports.Scene, _ map[ports.WindowID]ports.SurfaceContent) {
			s.Background = "#ffffff"
		},
		"image forgotten": func(o *Output, _ *ports.Scene, _ map[ports.WindowID]ports.SurfaceContent) {
			o.front.forget()
		},
	} {
		t.Run(name, func(t *testing.T) {
			o, k, _ := overlayOutput(t)
			o.cursor, o.vrrProp = nil, 0
			k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(88, nil).Maybe()
			o.overlay.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
			s, c := overlayScene()
			c[1] = ports.SurfaceContent{ID: 1, Seq: 1, Width: 100, Height: 100, Opaque: true, Solid: &ports.SolidColor{A: 1}}
			r := portsmocks.NewMockRenderer(t)
			r.EXPECT().UseTarget(mock.Anything).Return().Twice()
			var drawn []int
			r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, _ map[ports.WindowID]ports.SurfaceContent) (*os.File, error) {
				drawn = append(drawn, len(s.Windows))
				return nil, nil
			}).Twice()
			overlayFrames(t, o, r, s, c, 1)
			edit(o, &s, c)
			overlayFrames(t, o, r, s, c, 1)
			if len(drawn) != 2 || drawn[1] != 1 {
				t.Fatalf("rendered %v", drawn)
			}
		})
	}
}
