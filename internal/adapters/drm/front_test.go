package drm

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// overlayFrames submits the overlay scene once (composed), then with the
// overlay window's next buffers: the frames after the first only flip the
// overlay buffer.
func overlayFrames(t *testing.T, o *Output, r *portsmocks.MockRenderer, s ports.Scene, c map[domain.WindowID]ports.SurfaceContent, frames int) {
	t.Helper()
	for i := range frames {
		win := c[2]
		buf := *win.DMABuf
		buf.ID = uint64(9 + i)
		win.DMABuf, win.Seq = &buf, uint64(1+i)
		c[2] = win
		if _, err := o.submitFrame(context.Background(), r, s, c, map[domain.WindowID]uint64{}, nil, nil); err != nil {
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

// The desktop case through Run: a video (a dmabuf window on the overlay)
// next to a static window. Once the overlay is up, each new video buffer is
// one frame commit of the same composed image with no render, and each flip
// reports the video shown zero-copy at its latest content.
func TestRunVideoOnOverlayDoesNotRender(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o, k, commits, commitMu := testOutputMu(t)
		o.cursor, o.tearing, o.vrrProp = nil, false, 0
		props := map[string]uint32{"zpos": pZpos}
		for n, v := range planeProps {
			props[n] = v
		}
		o.primary.props = props
		o.stray = []*plane{{id: tOverlay, typ: planeOverlay, props: props, zposValue: 2, formats: []ports.DMABufFormat{{Format: fourccXRGB}}}}
		o.primary.formats = nil
		o.pickOverlay()
		flips := make(chan flipEvent, 1)
		o.flipped = flips
		r := portsmocks.NewMockRenderer(t)
		pipeBuf := func() ports.DMABuf {
			f, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			w.Close()
			return ports.DMABuf{Planes: []ports.DMABufPlane{{File: f}}}
		}
		r.EXPECT().SetHDR(float64(0)).Return().Maybe()
		r.EXPECT().ExportTargets(2, mock.Anything, false).Return([]ports.DMABuf{pipeBuf(), pipeBuf()}, nil).Once()
		r.EXPECT().UseTarget(mock.Anything).Return()
		var renders atomic.Int32
		r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(ports.Scene, map[domain.WindowID]ports.SurfaceContent) (*os.File, error) {
			renders.Add(1)
			return nil, nil
		})
		r.EXPECT().Close().Return().Once()
		var images atomic.Uint32
		k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).RunAndReturn(func(b *ports.DMABuf, _ uint32) (uint32, error) {
			if b.ID == 0 {
				return 70 + images.Add(1) - 1, nil // the output images
			}
			return uint32(100 + b.ID), nil
		})
		k.EXPECT().createBlob(mock.Anything).Return(99, nil)
		k.EXPECT().destroyBlob(mock.Anything).Return(nil).Maybe()
		k.EXPECT().rmFB(mock.Anything).Return(nil).Maybe()
		k.EXPECT().objProps(uint32(tOverlay), uint32(objPlane)).Return(map[string][2]uint64{}, nil).Maybe()
		frameCommits := func() []commitRec {
			commitMu.Lock()
			defer commitMu.Unlock()
			var out []commitRec
			for _, c := range *commits {
				if c.user&3 == userFrame {
					out = append(out, c)
				}
			}
			return out
		}
		scenes := make(chan ports.Scene, 1)
		contents := newContentFeed(t, 1)
		sink, presented := presentedSink(t, 64)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		setOutputRenderer(t, o, r)
		go func() {
			done <- o.Run(ctx, nil, make(chan bool), scenes, contents.in(), contents.ret, nil, sink, nil, nil)
		}()
		// Stop Run on every path, before the mocks check their calls.
		t.Cleanup(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(2 * time.Second):
				t.Error("Run did not stop")
			}
		})
		static := ports.SurfaceContent{ID: 1, Seq: 1, Width: 100, Height: 100, Opaque: true, Solid: &domain.SolidColor{A: 1}}
		video := func(n int) ports.SurfaceContent {
			return ports.SurfaceContent{ID: 2, Seq: uint64(n), Width: 100, Height: 100, Opaque: true,
				DMABuf: &ports.DMABuf{ID: uint64(n%3 + 1), Width: 100, Height: 100, Format: fourccXRGB}}
		}
		contents.send(static, video(1))
		scenes <- ports.Scene{Seq: 1, Scale: 1, OutputWidth: 200, OutputHeight: 100, Windows: []ports.SceneWindow{
			{ID: 1, Rect: domain.Rect{W: 100, H: 100}},
			{ID: 2, Rect: domain.Rect{X: 100, W: 100, H: 100}},
		}}
		// play delivers video frame n and flips every frame commit until a flip
		// shows it; it returns that commit and its report. (Seen is what the
		// output took in, which may run ahead of the flip.)
		flipped := 0
		vseq := uint32(0)
		play := func(n int) (commitRec, ports.OutputPresented) {
			t.Helper()
			contents.send(video(n))
			var c commitRec
			var got ports.OutputPresented
			synctest.Wait()
			for attempts := 0; attempts < 3; attempts++ {
				if cs := frameCommits(); len(cs) > flipped {
					c = cs[len(cs)-1]
					flipped = len(cs)
					vseq++
					flips <- flipEvent{crtc: tCrtc, user: c.user, when: time.Duration(vseq) * time.Millisecond, seq: vseq}
				}
				synctest.Wait()
				for len(presented) > 0 {
					p := <-presented
					if p.Flip != nil && p.Flip.Shows[2] == uint64(n) {
						got = p
						return c, got
					}
				}
			}
			t.Fatalf("video frame %d not presented after three commits", n)
			return c, got
		}
		// Warm up until the video is on the overlay over a composed image.
		n := 1
		for ; n < 10; n++ {
			c, _ := play(n)
			if v, _ := c.req.value(tOverlay, pFB); v != 0 {
				break
			}
		}
		base, commitsBefore := renders.Load(), len(frameCommits())
		const frames = 60
		for i := range frames {
			n++
			c, p := play(n)
			if v, _ := c.req.value(tOverlay, pFB); v != uint64(100+n%3+1) {
				t.Fatalf("video frame %d: overlay fb %d", i, v)
			}
			if _, ok := c.req.value(tPrimary, pFence); ok {
				t.Fatalf("video frame %d waits on a render", i)
			}
			// The flip reports the video shown zero-copy at its content.
			if p.Flip.ZeroCopy != 2 || p.Flip.Shows[2] != uint64(n) || p.Seen[1] != 1 {
				t.Fatalf("video frame %d: report %+v flip %+v", i, p, *p.Flip)
			}
		}
		if got := len(frameCommits()) - commitsBefore; got != frames {
			t.Fatalf("%d frame commits for %d video frames", got, frames)
		}
		if got := renders.Load() - base; got != 0 {
			t.Fatalf("%d renders for %d video frames, want 0", got, frames)
		}
		// Three buffers in rotation: each probed once, then remembered.
		commitMu.Lock()
		probes := 0
		for _, c := range *commits {
			if c.flags&atomicTestOnly != 0 {
				probes++
			}
		}
		commitMu.Unlock()
		if probes > 4 {
			t.Fatalf("%d overlay probes for %d video frames of 3 buffers", probes, frames)
		}
	})
}

// Refusals on the overlay-only path: a refused probe composes the whole
// scene at once; a refused commit drops the overlay and asks for a new frame,
// leaving the images and the queue as they were.
func TestSubmitFrameOverlayOnlyRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refuse  func(r *atomicReq, flags uint32) bool
		wantErr error
		renders int
	}{
		{"probe", func(_ *atomicReq, flags uint32) bool { return flags&atomicTestOnly != 0 }, nil, 1},
		{"commit", func(_ *atomicReq, flags uint32) bool { return flags&atomicTestOnly == 0 }, errOverlayDropped, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refusing := false
			o, k, _, _ := testOutputRules(t, func(r *atomicReq, flags uint32) error {
				if refusing && tc.refuse(r, flags) {
					return unix.EINVAL
				}
				return nil
			})
			props := map[string]uint32{"zpos": pZpos}
			for n, v := range planeProps {
				props[n] = v
			}
			o.primary.props, o.primary.formats = props, nil
			o.stray = []*plane{{id: tOverlay, typ: planeOverlay, props: props, zposValue: 2, formats: []ports.DMABufFormat{{Format: fourccXRGB}}}}
			o.pickOverlay()
			o.cursor, o.vrrProp = nil, 0
			k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).RunAndReturn(func(b *ports.DMABuf, _ uint32) (uint32, error) {
				return uint32(80 + b.ID), nil
			})
			s, c := overlayScene()
			r := portsmocks.NewMockRenderer(t)
			r.EXPECT().UseTarget(mock.Anything).Return()
			var drawn []int
			r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, _ map[domain.WindowID]ports.SurfaceContent) (*os.File, error) {
				drawn = append(drawn, len(s.Windows))
				return nil, nil
			})
			overlayFrames(t, o, r, s, c, 1) // composed, front image held
			back, queued := o.back, o.queued
			refusing = true
			win := c[2]
			buf := *win.DMABuf
			buf.ID, win.DMABuf, win.Seq = 20, &buf, 2
			c[2] = win
			_, err := o.submitFrame(context.Background(), r, s, c, map[domain.WindowID]uint64{}, nil, nil)
			if !errors.Is(err, tc.wantErr) && err != tc.wantErr {
				t.Fatalf("err %v, want %v", err, tc.wantErr)
			}
			if got := len(drawn) - 1; got != tc.renders {
				t.Fatalf("%d renders, want %d", got, tc.renders)
			}
			if tc.renders == 1 && drawn[1] != 2 {
				t.Fatalf("composed %d windows, want the whole scene", drawn[1])
			}
			if tc.wantErr != nil {
				if o.back != back || o.queued != queued || o.clientFBs[20].overlayFailed != "cursor_conflict" {
					t.Fatalf("back %d queued %d reason %q", o.back, o.queued, o.clientFBs[20].overlayFailed)
				}
				// The retry composes the window.
				refusing = false
				if _, err := o.submitFrame(context.Background(), r, s, c, map[domain.WindowID]uint64{}, nil, nil); err != nil {
					t.Fatal(err)
				}
				if drawn[len(drawn)-1] != 2 {
					t.Fatalf("retry composed %d windows", drawn[len(drawn)-1])
				}
			}
		})
	}
}

// A commit result with the overlay on: refusals the overlay causes ask for a
// frame without it and leave the queue; a commit queues the buffer.
func TestFinishOverlayCommit(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name       string
		err        error
		want       error
		wantQueued uint64
		reason     string
	}{
		{"committed", nil, nil, 9, ""},
		{"colour refused", errColorRefused, errOverlayDropped, 1, "color_refused"},
		{"overlay conflict", unix.EINVAL, errOverlayDropped, 1, "cursor_conflict"},
		{"other", boom, boom, 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _, _ := testOutput(t)
			o.queued = 1
			o.clientFBs[9] = &clientFB{}
			if err := o.finishOverlayCommit(tc.err, 9); err != tc.want {
				t.Fatalf("err %v, want %v", err, tc.want)
			}
			if o.queued != tc.wantQueued || o.overlayReason != tc.reason {
				t.Fatalf("queued %d reason %q", o.queued, o.overlayReason)
			}
		})
	}
}

// A frame whose buffer KMS took before is not probed again.
func TestOverlayProbeCached(t *testing.T) {
	o, k, commits := overlayOutput(t)
	o.cursor.image = true
	o.cursor.Move(10, 10)
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
	o.cursor.Move(20, 10)
	o.testOverlay(70, ov)
	if len(*commits) != 4 {
		t.Fatalf("%d probes, want 4", len(*commits))
	}
	// The probe tested the cursor where the cache remembers it.
	if v, _ := (*commits)[3].req.value(tCursor, 14); v != 20-uint64(o.cursor.hotX) {
		t.Fatalf("probe cursor x %d", v)
	}
	// New colour state (HDR brightness) is probed again.
	o.colorMult++
	o.testOverlay(70, ov)
	if len(*commits) != 5 {
		t.Fatalf("%d probes after a colour change, want 5", len(*commits))
	}
	// A removed client framebuffer may give its id to another buffer.
	k.EXPECT().rmFB(uint32(88)).Return(nil).Once()
	o.dropClientFBs(time.Now(), true)
	o.testOverlay(70, ov)
	if len(*commits) != 6 {
		t.Fatal("probe cached across a framebuffer removal")
	}
	// A modeset (VT resume) forgets them.
	k.EXPECT().createBlob(mock.Anything).Return(99, nil).Once()
	k.EXPECT().objProps(uint32(tOverlay), uint32(objPlane)).Return(map[string][2]uint64{}, nil).Maybe()
	if err := o.modeset(); err != nil {
		t.Fatal(err)
	}
	before := len(*commits)
	o.testOverlay(70, ov)
	if len(*commits) != before+1 {
		t.Fatal("probe cached across a modeset")
	}
}

// Anything but the overlay window changing composes: another window's
// content, the scene, or an image drawn by another path.
func TestSubmitFrameOverlayOnlyNeedsSameImage(t *testing.T) {
	for name, edit := range map[string]func(o *Output, s *ports.Scene, c map[domain.WindowID]ports.SurfaceContent){
		"other window content": func(_ *Output, _ *ports.Scene, c map[domain.WindowID]ports.SurfaceContent) {
			w := c[1]
			w.Seq++
			c[1] = w
		},
		"scene": func(_ *Output, s *ports.Scene, _ map[domain.WindowID]ports.SurfaceContent) {
			s.Background = "#ffffff"
		},
		"image forgotten": func(o *Output, _ *ports.Scene, _ map[domain.WindowID]ports.SurfaceContent) {
			o.front.forget()
		},
		"layer content": func(_ *Output, _ *ports.Scene, c map[domain.WindowID]ports.SurfaceContent) {
			l := c[5]
			l.Seq++
			c[5] = l
		},
		// The scene's slices rewritten in place by their owner: what the
		// image holds is a copy.
		"scene slice rewritten": func(_ *Output, s *ports.Scene, _ map[domain.WindowID]ports.SurfaceContent) {
			s.Windows[0].Rect.W = 90
		},
	} {
		t.Run(name, func(t *testing.T) {
			o, k, _ := overlayOutput(t)
			o.cursor, o.vrrProp = nil, 0
			k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(88, nil).Maybe()
			o.overlay.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
			s, c := overlayScene()
			s.Layers = []ports.SceneLayer{{ID: 5, Layer: domain.LayerBottom, Rect: domain.Rect{W: 10, H: 10}}}
			c[1] = ports.SurfaceContent{ID: 1, Seq: 1, Width: 100, Height: 100, Opaque: true, Solid: &domain.SolidColor{A: 1}}
			c[5] = ports.SurfaceContent{ID: 5, Seq: 1, Width: 10, Height: 10, Opaque: true, Solid: &domain.SolidColor{A: 1}}
			r := portsmocks.NewMockRenderer(t)
			r.EXPECT().UseTarget(mock.Anything).Return().Twice()
			var drawn []int
			r.EXPECT().Render(mock.Anything, mock.Anything).RunAndReturn(func(s ports.Scene, _ map[domain.WindowID]ports.SurfaceContent) (*os.File, error) {
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
