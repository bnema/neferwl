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
	if _, reason := scanoutCandidate(s, contents, 200, 100, nil); reason != "workspace_clip" {
		t.Fatalf("fullscreen bypassed clip: %q", reason)
	}
	if fullscreenShown(&s) {
		t.Fatal("clipped fullscreen counted as output-covering")
	}
}

func TestOverlayWorkspaceClip(t *testing.T) {
	s, contents := overlayScene()
	s.WorkspaceClip = ports.Rect{X: 50, W: 100, H: 100}
	if _, _, _, reason := overlayCandidate(s, contents, false, nil, nil); reason != "workspace_clip" {
		t.Fatalf("overflowing workspace overlay: %q", reason)
	}
	s.WorkspaceClip = ports.Rect{X: 100, W: 100, H: 100}
	if win, _, _, reason := overlayCandidate(s, contents, false, nil, nil); reason != "" || win.ID != 2 {
		t.Fatalf("contained workspace overlay: %v %q", win.ID, reason)
	}
}

// A tile cut by a panel (TileInset) is composed: the plane would show it
// whole, over the panel. A tile wholly under the panel is not drawn, so it
// neither takes the plane nor blocks the visible tile.
func TestOverlayTileInset(t *testing.T) {
	s, contents := overlayScene()
	s.TileInset = ports.Insets{Bottom: 20}
	if _, _, _, reason := overlayCandidate(s, contents, false, nil, nil); reason != "tile_clip" {
		t.Fatalf("tile under a panel on the overlay: %q", reason)
	}
	// The next cascade band starts under the panel.
	s, contents = overlayScene()
	s.OutputHeight = 120
	s.TileInset = ports.Insets{Bottom: 20}
	s.Windows = append(s.Windows, ports.SceneWindow{ID: 3, Rect: ports.Rect{Y: 100, W: 100, H: 100}})
	contents[3] = ports.SurfaceContent{ID: 3, Width: 100, Height: 100, Opaque: true, DMABuf: &ports.DMABuf{ID: 10, Width: 100, Height: 100, Format: fourccXRGB}}
	if win, _, _, reason := overlayCandidate(s, contents, false, nil, nil); reason != "" || win.ID != 2 {
		t.Fatalf("visible tile with one under the panel: %v %q", win.ID, reason)
	}
}

// A fullscreen window is scanned out when the only other window is a tile
// the scene does not draw (no room left by the panels).
func TestScanoutIgnoresUndrawnTile(t *testing.T) {
	s, contents := fullscreenScene()
	s.TileInset = ports.Insets{Bottom: 100}
	s.Windows = append([]ports.SceneWindow{{ID: 2, Rect: ports.Rect{W: 200, H: 100}}}, s.Windows...)
	if _, reason := scanoutCandidate(s, contents, 200, 100, nil); reason != "" {
		t.Fatalf("undrawn tile blocked scanout: %q", reason)
	}
}

func TestOverlayCandidate(t *testing.T) {
	s, c := overlayScene()
	if w, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "" || w.ID != 2 {
		t.Fatalf("candidate %v %q", w.ID, reason)
	}
	// A rotated output composes: planes are not rotated.
	rs, rc := overlayScene()
	rs.Transform = 1
	if _, _, _, reason := overlayCandidate(rs, rc, false, nil, nil); reason != "output_transform" {
		t.Fatalf("rotated output reason %q", reason)
	}
	c[2] = ports.SurfaceContent{ID: 2, Width: 100, Height: 100, Opaque: true, DMABuf: &ports.DMABuf{Format: fourccNV12}}
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "no_candidate" {
		t.Fatalf("YUV overlay reason %q", reason)
	}
	// A transformed buffer is composed: the plane would show it unrotated.
	_, c = overlayScene()
	rotated := c[2]
	rotated.Transform = 1
	c[2] = rotated
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "no_candidate" {
		t.Fatalf("transformed overlay reason %q", reason)
	}
	_, c = overlayScene()
	s.Windows = append(s.Windows, ports.SceneWindow{ID: 3, Rect: ports.Rect{X: 150, W: 10, H: 10}})
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "window_above" {
		t.Fatalf("reason %q", reason)
	}
	s, _ = overlayScene()
	s.Scale = 1.5
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "scaled" {
		t.Fatalf("reason %q", reason)
	}
	s, _ = overlayScene()
	s.Layers = []ports.SceneLayer{{ID: 5, Layer: ports.LayerOverlay, Rect: ports.Rect{X: 150, W: 5, H: 5}}}
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "layer_above" {
		t.Fatalf("reason %q", reason)
	}
	// The dim veil is composed under the float: nothing leaves for the plane.
	s, _ = overlayScene()
	s.Dim = 0.3
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "dim" {
		t.Fatalf("dim reason %q", reason)
	}
	// A dimmed window (a peeking stashed one) needs its veil composed.
	s, _ = overlayScene()
	s.Windows[len(s.Windows)-1].Dim = 0.5
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "no_candidate" {
		t.Fatalf("dimmed window reason %q", reason)
	}
	// A fading window needs its translucency composed.
	s, _ = overlayScene()
	s.Windows[len(s.Windows)-1].Fade = 0.5
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "no_candidate" {
		t.Fatalf("fading window reason %q", reason)
	}
	// A zoomed (animating) window is drawn smaller than its buffer.
	s, _ = overlayScene()
	s.Windows[len(s.Windows)-1].Zoom = 0.9
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "no_candidate" {
		t.Fatalf("zoomed window reason %q", reason)
	}
	// A window with the focus effect needs it composed.
	s, _ = overlayScene()
	s.Windows[len(s.Windows)-1].FocusEffect = 0.05
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "no_candidate" {
		t.Fatalf("pulsing window reason %q", reason)
	}
	// An overview preview is drawn smaller than its buffer: composed.
	s, _ = overlayScene()
	s.Windows[len(s.Windows)-1].Preview = 0.5
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "no_candidate" {
		t.Fatalf("preview reason %q", reason)
	}
	// A viewport crop is composed: the plane would show the whole buffer.
	s, c = overlayScene()
	cropped := c[2]
	cropped.Source = [4]float32{10, 10, 50, 50}
	c[2] = cropped
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "no_candidate" {
		t.Fatalf("cropped overlay reason %q", reason)
	}
	// A full-buffer source is no crop.
	cropped.Source = [4]float32{0, 0, 100, 100}
	c[2] = cropped
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "" {
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
	s.Windows = append([]ports.SceneWindow{{ID: 7, Popup: true, Rect: ports.Rect{X: 120, W: 5, H: 5}}}, s.Windows...)
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "window_above" {
		t.Fatalf("popup: %q", reason)
	}
	s, _ = overlayScene()
	s.Separators = []ports.Separator{{Rect: ports.Rect{X: 100, W: 2, H: 100}}}
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "border" {
		t.Fatalf("border: %q", reason)
	}
	s, _ = overlayScene()
	s.Border.Width = 2
	s.Windows[1].Inset = ports.SideLeft
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "border" {
		t.Fatalf("inset content: %q", reason)
	}
}

// What is drawn elsewhere on screen stays composed on the primary plane: a
// bar, a popup, another window's lines or a window that do not touch the
// candidate leave it on the overlay.
func TestOverlayIgnoresWhatDoesNotCoverIt(t *testing.T) {
	s, c := overlayScene()
	s.Layers = []ports.SceneLayer{{ID: 5, Layer: ports.LayerTop, Rect: ports.Rect{W: 100, H: 10}}}
	s.Windows = append(s.Windows,
		ports.SceneWindow{ID: 7, Popup: true, Rect: ports.Rect{X: 10, Y: 10, W: 20, H: 20}},
		ports.SceneWindow{ID: 8, Floating: true, Rect: ports.Rect{X: 40, Y: 40, W: 20, H: 20}})
	s.Separators = []ports.Separator{{Rect: ports.Rect{X: 98, W: 2, H: 100}, Window: 1}}
	if w, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "" || w.ID != 2 {
		t.Fatalf("candidate %v %q", w.ID, reason)
	}
	// Touching edges do not cover it.
	s.Layers[0].Rect = ports.Rect{W: 100, H: 100}
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "" {
		t.Fatalf("adjacent layer: %q", reason)
	}
	s.Layers[0].Rect = ports.Rect{W: 101, H: 10}
	if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != "layer_above" {
		t.Fatalf("overlapping layer: %q", reason)
	}
}

// Draw order decides: a window listed before the candidate is under it, one
// after is over it; a popup is over it wherever it is listed, unless hidden.
func TestOverlayStackingOrder(t *testing.T) {
	under := ports.SceneWindow{ID: 8, Floating: true, Below: true, Rect: ports.Rect{X: 120, W: 20, H: 20}}
	over := under
	over.Below = false
	popup := ports.SceneWindow{ID: 7, Popup: true, Rect: ports.Rect{X: 120, W: 20, H: 20}}
	for name, tc := range map[string]struct {
		edit func(*ports.Scene)
		want string
	}{
		"window under": {func(s *ports.Scene) { s.Windows = append([]ports.SceneWindow{under}, s.Windows...) }, ""},
		"window over":  {func(s *ports.Scene) { s.Windows = append(s.Windows, over) }, "window_above"},
		"popup after":  {func(s *ports.Scene) { s.Windows = append(s.Windows, popup) }, "window_above"},
		"hidden popup": {func(s *ports.Scene) { p := popup; p.Hidden = true; s.Windows = append(s.Windows, p) }, ""},
		"popup touching": {func(s *ports.Scene) {
			p := popup
			p.Rect.X = 80
			s.Windows = append(s.Windows, p)
		}, ""},
	} {
		s, c := overlayScene()
		tc.edit(&s)
		if _, _, _, reason := overlayCandidate(s, c, false, nil, nil); reason != tc.want {
			t.Errorf("%s: reason %q, want %q", name, reason, tc.want)
		}
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
