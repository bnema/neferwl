package drm

import (
	"os"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

const pZpos = 30

// overlayOutput is a test output with an overlay plane above the primary.
func overlayOutput(t *testing.T, errs ...error) (*Output, *mockkms, *[]commitRec) {
	o, k, commits := testOutput(t, errs...)
	props := map[string]uint32{"zpos": pZpos}
	for n, v := range planeProps {
		props[n] = v
	}
	o.primary.props = props
	o.stray = []*plane{{id: tOverlay, typ: planeOverlay, props: props, zposValue: 2}}
	o.primary.formats = nil // the overlay's own formats decide
	o.pickOverlay()
	if o.overlay == nil {
		t.Fatal("no overlay picked")
	}
	return o, k, commits
}

// A 1:1 opaque dmabuf window with nothing above goes on the overlay.
func overlayScene() (ports.Scene, map[ports.WindowID]ports.SurfaceContent) {
	s := ports.Scene{Seq: 1, Scale: 1, OutputWidth: 200, OutputHeight: 100, Windows: []ports.SceneWindow{
		{ID: 1, Rect: ports.Rect{W: 100, H: 100}},
		{ID: 2, Rect: ports.Rect{X: 100, W: 100, H: 100}},
	}}
	return s, map[ports.WindowID]ports.SurfaceContent{
		2: {ID: 2, Width: 100, Height: 100, Opaque: true, DMABuf: &ports.DMABuf{ID: 9, Width: 100, Height: 100, Format: fourccXRGB}},
	}
}

func TestScanoutWorkspaceClipCannotBeBypassed(t *testing.T) {
	s, contents := fullscreenScene()
	s.WorkspaceClip = ports.Rect{X: 50, W: 100, H: 100}
	if _, reason := scanoutCandidate(s, contents, 200, 100); reason != "workspace_clip" {
		t.Fatalf("fullscreen bypassed clip: %q", reason)
	}
	if fullscreenShown(&s) {
		t.Fatal("clipped fullscreen counted as output-covering")
	}
}

func TestOverlayWorkspaceClip(t *testing.T) {
	s, contents := overlayScene()
	s.WorkspaceClip = ports.Rect{X: 50, W: 100, H: 100}
	if _, _, reason := overlayCandidate(s, contents, false, nil); reason != "workspace_clip" {
		t.Fatalf("overflowing workspace overlay: %q", reason)
	}
	s.WorkspaceClip = ports.Rect{X: 100, W: 100, H: 100}
	if win, _, reason := overlayCandidate(s, contents, false, nil); reason != "" || win.ID != 2 {
		t.Fatalf("contained workspace overlay: %v %q", win.ID, reason)
	}
}

func TestOverlayCandidate(t *testing.T) {
	s, c := overlayScene()
	if w, _, reason := overlayCandidate(s, c, false, nil); reason != "" || w.ID != 2 {
		t.Fatalf("candidate %v %q", w.ID, reason)
	}
	// A rotated output composes: planes are not rotated.
	rs, rc := overlayScene()
	rs.Transform = 1
	if _, _, reason := overlayCandidate(rs, rc, false, nil); reason != "output_transform" {
		t.Fatalf("rotated output reason %q", reason)
	}
	c[2] = ports.SurfaceContent{ID: 2, Width: 100, Height: 100, Opaque: true, DMABuf: &ports.DMABuf{Format: fourccNV12}}
	if _, _, reason := overlayCandidate(s, c, false, nil); reason != "no_candidate" {
		t.Fatalf("YUV overlay reason %q", reason)
	}
	// A transformed buffer is composed: the plane would show it unrotated.
	_, c = overlayScene()
	rotated := c[2]
	rotated.Transform = 1
	c[2] = rotated
	if _, _, reason := overlayCandidate(s, c, false, nil); reason != "no_candidate" {
		t.Fatalf("transformed overlay reason %q", reason)
	}
	_, c = overlayScene()
	s.Windows = append(s.Windows, ports.SceneWindow{ID: 3, Rect: ports.Rect{W: 10, H: 10}})
	if _, _, reason := overlayCandidate(s, c, false, nil); reason != "window_above" {
		t.Fatalf("reason %q", reason)
	}
	s, _ = overlayScene()
	s.Scale = 1.5
	if _, _, reason := overlayCandidate(s, c, false, nil); reason != "scaled" {
		t.Fatalf("reason %q", reason)
	}
	s, _ = overlayScene()
	s.Layers = []ports.SceneLayer{{ID: 5, Layer: ports.LayerOverlay, Rect: ports.Rect{W: 5, H: 5}}}
	if _, _, reason := overlayCandidate(s, c, false, nil); reason != "layer_above" {
		t.Fatalf("reason %q", reason)
	}
	// The dim veil is composed under the float: nothing leaves for the plane.
	s, _ = overlayScene()
	s.Dim = 0.3
	if _, _, reason := overlayCandidate(s, c, false, nil); reason != "dim" {
		t.Fatalf("dim reason %q", reason)
	}
	// A dimmed window (a peeking stashed one) needs its veil composed.
	s, _ = overlayScene()
	s.Windows[len(s.Windows)-1].Dim = 0.5
	if _, _, reason := overlayCandidate(s, c, false, nil); reason != "no_candidate" {
		t.Fatalf("dimmed window reason %q", reason)
	}
	// A window with the focus effect needs it composed.
	s, _ = overlayScene()
	s.Windows[len(s.Windows)-1].FocusEffect = 0.05
	if _, _, reason := overlayCandidate(s, c, false, nil); reason != "no_candidate" {
		t.Fatalf("pulsing window reason %q", reason)
	}
	// An overview preview is drawn smaller than its buffer: composed.
	s, _ = overlayScene()
	s.Windows[len(s.Windows)-1].Preview = 0.5
	if _, _, reason := overlayCandidate(s, c, false, nil); reason != "no_candidate" {
		t.Fatalf("preview reason %q", reason)
	}
	// A viewport crop is composed: the plane would show the whole buffer.
	s, c = overlayScene()
	cropped := c[2]
	cropped.Source = [4]float32{10, 10, 50, 50}
	c[2] = cropped
	if _, _, reason := overlayCandidate(s, c, false, nil); reason != "no_candidate" {
		t.Fatalf("cropped overlay reason %q", reason)
	}
	// A full-buffer source is no crop.
	cropped.Source = [4]float32{0, 0, 100, 100}
	c[2] = cropped
	if _, _, reason := overlayCandidate(s, c, false, nil); reason != "" {
		t.Fatalf("full source reason %q", reason)
	}
}

// The overlay is used only on SDR outputs: PQ content there needs the
// renderer's tone mapping.
func TestOverlaySkipsPQContent(t *testing.T) {
	o, _, _ := overlayOutput(t)
	o.overlay.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	s, c := overlayScene()
	pq := c[2]
	pq.Color = ports.SurfaceColor{TF: ports.ColorTFPQ, Primaries: ports.ColorPrimariesBT2020}
	c[2] = pq
	if ov, rest := o.overlayFrame(s, c); ov.fb != 0 || len(rest.Windows) != 2 {
		t.Fatalf("PQ overlay %+v rest %+v", ov, rest.Windows)
	}
}

// The overlay is chosen and the composed scene leaves its window out;
// the frame commit carries the plane at the window's place.
func TestOverlayChosen(t *testing.T) {
	o, k, commits := overlayOutput(t)
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(88, nil).Once()
	o.overlay.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	s, c := overlayScene()
	ov, rest := o.overlayFrame(s, c)
	if ov.fb != 88 || len(rest.Windows) != 1 || rest.Windows[0].ID != 1 {
		t.Fatalf("overlay %+v rest %+v", ov, rest.Windows)
	}
	if !o.testOverlay(70, ov) {
		t.Fatal("test refused")
	}
	if err := o.commitWith(70, nil, false, false, pendingFrame{queued: ov.buf}, ov); err != nil {
		t.Fatal(err)
	}
	last := (*commits)[len(*commits)-1]
	if v, _ := last.req.value(tOverlay, pFB); v != 88 {
		t.Fatalf("overlay fb %d", v)
	}
	if v, _ := last.req.value(tOverlay, 14); v != 100 {
		t.Fatalf("overlay x %d", v)
	}
	if o.overlayOn != 9 {
		t.Fatalf("overlayOn %d", o.overlayOn)
	}
}

// A TEST_ONLY refusal composes the window and is cached on the buffer.
func TestOverlayTestRefusedFallsBack(t *testing.T) {
	o, k, _ := overlayOutput(t, unix.EINVAL)
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(88, nil).Once()
	o.overlay.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	s, c := overlayScene()
	ov, _ := o.overlayFrame(s, c)
	if o.testOverlay(70, ov) {
		t.Fatal("refusal taken")
	}
	if o.overlayReason != "overlay_refused" {
		t.Fatalf("refusal reason %q", o.overlayReason)
	}
	if ov2, rest := o.overlayFrame(s, c); ov2.fb != 0 || len(rest.Windows) != 2 {
		t.Fatal("refused buffer tried again")
	}
	// The overlay's refusal does not bar direct scanout of the buffer.
	o.primary.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	if fb, reason := o.scanoutFB(c[2].DMABuf, time.Now()); fb != 88 || reason != "" {
		t.Fatalf("scanout after overlay refusal: %d %q", fb, reason)
	}
}

// Popups are drawn above every window: one listed first still blocks the
// overlay; a bordered window does too.
func TestOverlayPopupAndBorder(t *testing.T) {
	s, c := overlayScene()
	s.Windows = append([]ports.SceneWindow{{ID: 7, Popup: true, Rect: ports.Rect{W: 5, H: 5}}}, s.Windows...)
	if _, _, reason := overlayCandidate(s, c, false, nil); reason != "window_above" {
		t.Fatalf("popup: %q", reason)
	}
	s, _ = overlayScene()
	s.Separators = []ports.Separator{{Rect: ports.Rect{W: 2, H: 2}}}
	if _, _, reason := overlayCandidate(s, c, false, nil); reason != "border" {
		t.Fatalf("border: %q", reason)
	}
}

// A cursor commit refused with the overlay on drops the overlay: the
// next frame is composed.
func TestOverlayCursorConflictDropsOverlay(t *testing.T) {
	o, k, _ := overlayOutput(t, nil, unix.EINVAL)
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(88, nil).Once()
	o.overlay.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	s, c := overlayScene()
	ov, _ := o.overlayFrame(s, c)
	if err := o.commitWith(70, nil, false, false, pendingFrame{}, ov); err != nil {
		t.Fatal(err)
	}
	o.frame.endPending()
	o.cursor.image = true
	o.cursor.Move(150, 50)
	if err := o.commitState(false); err != errOverlayDropped {
		t.Fatalf("err %v", err)
	}
	if ov2, _ := o.overlayFrame(s, c); ov2.fb != 0 || o.overlayReason != "cursor_conflict" {
		t.Fatalf("overlay kept: %+v %q", ov2, o.overlayReason)
	}
}

// An explicit-sync window on the overlay: the plane carries a duplicate of
// the client's acquire fence, closed once the frame is committed.
func TestOverlayCarriesAcquireFence(t *testing.T) {
	o, k, commits := overlayOutput(t)
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(88, nil).Once()
	o.overlay.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	s, c := overlayScene()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	win := c[2]
	win.Acquire = r
	c[2] = win
	ov, _ := o.overlayFrame(s, c)
	if ov.acquire == nil || ov.acquire.Fd() == r.Fd() {
		t.Fatal("overlay has no duplicate of the acquire fence")
	}
	fd := ov.acquire.Fd()
	if err := o.commitWith(70, nil, false, false, pendingFrame{queued: ov.buf}, ov); err != nil {
		t.Fatal(err)
	}
	ov.close()
	last := (*commits)[len(*commits)-1]
	if v, ok := last.req.value(tOverlay, pFence); !ok || v != uint64(fd) {
		t.Fatalf("overlay IN_FENCE_FD %d %v, want %d", v, ok, fd)
	}
	if _, err := ov.acquire.Stat(); err == nil {
		t.Fatal("duplicate left open")
	}
}
