package drm

import (
	"os"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

func TestScanoutCandidate(t *testing.T) {
	full := ports.SceneWindow{ID: 1, Rect: ports.Rect{W: 100, H: 50}, Fullscreen: true}
	scene := ports.Scene{OutputWidth: 100, OutputHeight: 50, Windows: []ports.SceneWindow{full}}
	buf := &ports.DMABuf{ID: 9, Width: 200, Height: 100, Format: fourccARGB, Planes: []ports.DMABufPlane{{}}}
	game := ports.SurfaceContent{ID: 1, Width: 200, Height: 100, LogicalW: 100, LogicalH: 50, DMABuf: buf}
	for _, tc := range []struct {
		name   string
		scene  func(s *ports.Scene)
		cont   func(c *ports.SurfaceContent)
		reason string
	}{
		{"scanout", nil, nil, ""},
		{"hidden others ignored", func(s *ports.Scene) {
			s.Windows = append(s.Windows, ports.SceneWindow{ID: 2, Hidden: true})
		}, nil, ""},
		{"no window", func(s *ports.Scene) { s.Windows = nil }, nil, "no_fullscreen"},
		{"tiled", func(s *ports.Scene) { s.Windows[0].Fullscreen = false }, nil, "other_windows"},
		{"float above", func(s *ports.Scene) {
			s.Windows = append(s.Windows, ports.SceneWindow{ID: 2, Rect: ports.Rect{W: 10, H: 10}})
		}, nil, "other_windows"},
		{"not covering", func(s *ports.Scene) { s.Windows[0].Rect.W = 90 }, nil, "other_windows"},
		{"overlay layer", func(s *ports.Scene) {
			s.Layers = []ports.SceneLayer{{ID: 3, Layer: ports.LayerOverlay, Rect: ports.Rect{W: 10, H: 10}}}
		}, nil, "overlay_surface"},
		{"top layer", func(s *ports.Scene) {
			s.Layers = []ports.SceneLayer{{ID: 3, Layer: ports.LayerTop, Rect: ports.Rect{W: 10, H: 10}}}
		}, nil, "overlay_surface"},
		{"background layer", func(s *ports.Scene) {
			s.Layers = []ports.SceneLayer{{ID: 3, Layer: ports.LayerBackground, Rect: ports.Rect{W: 10, H: 10}}}
		}, nil, ""},
		{"shm", nil, func(c *ports.SurfaceContent) { c.DMABuf, c.SHM = nil, &ports.SHMBuffer{} }, "not_dmabuf"},
		{"subsurface", nil, func(c *ports.SurfaceContent) { c.Children = []ports.Subsurface{{}} }, "subsurfaces"},
		{"buffer transform", nil, func(c *ports.SurfaceContent) { c.Transform = 2 }, "buffer_transform"},
		{"scaled", nil, func(c *ports.SurfaceContent) { c.Width = 100 }, "size_mismatch"},
		{"cropped", nil, func(c *ports.SurfaceContent) { c.Geometry = ports.Rect{X: 5, W: 90, H: 50} }, "geometry_crop"},
		{"client ignores scale", nil, func(c *ports.SurfaceContent) { c.LogicalW = 200 }, "logical_mismatch"},
		{"whole geometry", nil, func(c *ports.SurfaceContent) { c.Geometry = ports.Rect{W: 100, H: 50} }, ""},
		{"wine client subsurface", nil, wineTree(nil), ""},
		{"client subsurface translucent", nil, wineTree(func(ch *ports.Subsurface) { ch.Opaque = false }), "subsurface_translucent"},
		{"client subsurface shm", nil, wineTree(func(ch *ports.Subsurface) { ch.DMABuf, ch.SHM = nil, &ports.SHMBuffer{} }), "not_dmabuf"},
		{"client subsurface offset", nil, wineTree(func(ch *ports.Subsurface) { ch.X = 4 }), "subsurface_offset"},
		{"client subsurface below root", nil, wineTree(func(ch *ports.Subsurface) { ch.Below = true }), "subsurface_below"},
		{"client subsurface cropped", nil, wineTree(func(ch *ports.Subsurface) { ch.Source = [4]float32{0, 0, 100, 50} }), "source_crop"},
		{"client subsurface full source", nil, wineTree(func(ch *ports.Subsurface) { ch.Source = [4]float32{0, 0, 200, 100} }), ""},
		{"root cropped", nil, func(c *ports.SurfaceContent) { c.Source = [4]float32{10, 0, 190, 100} }, "source_crop"},
		{"client subsurface scaled", nil, wineTree(func(ch *ports.Subsurface) { ch.Width = 100 }), "size_mismatch"},
		{"client subsurface small", nil, wineTree(func(ch *ports.Subsurface) { ch.LogicalW = 90 }), "logical_mismatch"},
		{"two subsurfaces", nil, func(c *ports.SurfaceContent) {
			wineTree(nil)(c)
			c.Children = append(c.Children, c.Children[0])
		}, "subsurfaces"},
	} {
		s := scene
		s.Windows = append([]ports.SceneWindow(nil), scene.Windows...)
		c := game
		if tc.scene != nil {
			tc.scene(&s)
		}
		if tc.cont != nil {
			tc.cont(&c)
		}
		if _, reason := scanoutCandidate(s, map[ports.WindowID]ports.SurfaceContent{1: c}, 200, 100); reason != tc.reason {
			t.Errorf("%s: reason %q, want %q", tc.name, reason, tc.reason)
		}
	}
}

// wineTree turns the test content into Wine's layout: the root is a wl_shm
// frame and the game is one opaque GPU subsurface over the client area.
func wineTree(edit func(ch *ports.Subsurface)) func(c *ports.SurfaceContent) {
	return func(c *ports.SurfaceContent) {
		ch := ports.Subsurface{SurfaceContent: *c}
		ch.Opaque = true
		if edit != nil {
			edit(&ch)
		}
		c.DMABuf, c.SHM = nil, &ports.SHMBuffer{}
		c.Children = []ports.Subsurface{ch}
	}
}

// Wine's game subsurface is scanned out in place of the window: the
// content keeps the window's ID and Seq and takes the subsurface buffer.
func TestScanoutCandidateClientSubsurface(t *testing.T) {
	scene := ports.Scene{OutputWidth: 100, OutputHeight: 50, Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 100, H: 50}, Fullscreen: true}}}
	game := &ports.DMABuf{ID: 9, Width: 200, Height: 100, Format: fourccXRGB, Planes: []ports.DMABufPlane{{}}}
	acquire := &os.File{}
	root := ports.SurfaceContent{ID: 1, Seq: 7, Width: 200, Height: 100, LogicalW: 100, LogicalH: 50, SHM: &ports.SHMBuffer{},
		Geometry: ports.Rect{W: 100, H: 50},
		Children: []ports.Subsurface{{SurfaceContent: ports.SurfaceContent{Seq: 3, Width: 200, Height: 100, LogicalW: 100, LogicalH: 50, Opaque: true, DMABuf: game, Async: true, Acquire: acquire}}}}
	c, reason := scanoutCandidate(scene, map[ports.WindowID]ports.SurfaceContent{1: root}, 200, 100)
	if reason != "" || c.DMABuf != game || c.ID != 1 || c.Seq != 7 || !c.Async || c.Acquire != acquire || c.Geometry != (ports.Rect{}) {
		t.Fatalf("candidate %+v, reason %q", c, reason)
	}
}

// VRR follows a visible fullscreen window covering the output, even
// when other windows or layers force composition.
func TestFullscreenShown(t *testing.T) {
	full := ports.SceneWindow{ID: 1, Rect: ports.Rect{W: 100, H: 50}, Fullscreen: true}
	for _, tc := range []struct {
		name string
		wins []ports.SceneWindow
		want bool
	}{
		{"fullscreen", []ports.SceneWindow{full}, true},
		{"with dialog above", []ports.SceneWindow{full, {ID: 2, Rect: ports.Rect{W: 10, H: 10}, Floating: true}}, true},
		{"hidden", []ports.SceneWindow{{ID: 1, Rect: full.Rect, Fullscreen: true, Hidden: true}}, false},
		{"off screen", []ports.SceneWindow{{ID: 1, Rect: ports.Rect{X: 100, W: 100, H: 50}, Fullscreen: true}}, false},
		{"tiled covering", []ports.SceneWindow{{ID: 1, Rect: full.Rect}}, false},
		{"none", nil, false},
	} {
		s := ports.Scene{OutputWidth: 100, OutputHeight: 50, Windows: tc.wins}
		if got := fullscreenShown(&s); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// fullscreenShown runs on every composed frame of a VRR output.
func TestFullscreenShownAllocations(t *testing.T) {
	s := tiledFullscreenScene()
	if allocs := testing.AllocsPerRun(100, func() { fullscreenShown(&s) }); allocs != 0 {
		t.Errorf("fullscreenShown: %.1f allocs, want 0", allocs)
	}
}

func BenchmarkFullscreenShown(b *testing.B) {
	s := tiledFullscreenScene()
	b.ReportAllocs()
	for b.Loop() {
		fullscreenShown(&s)
	}
}

// tiledFullscreenScene is 64 tiled windows with the fullscreen one last
// (worst case for the walk).
func tiledFullscreenScene() ports.Scene {
	s := ports.Scene{OutputWidth: 3840, OutputHeight: 2160}
	for i := range 64 {
		s.Windows = append(s.Windows, ports.SceneWindow{ID: ports.WindowID(i + 1), Rect: ports.Rect{X: i * 60, W: 60, H: 2160}})
	}
	s.Windows = append(s.Windows, ports.SceneWindow{ID: 99, Rect: ports.Rect{W: 3840, H: 2160}, Fullscreen: true})
	return s
}

// A buffer scans out when the primary plane lists its format (or the
// opaque variant) with its modifier; the tranche offers only formats the
// renderer also composes.
func TestScanoutFormatFollowsInFormats(t *testing.T) {
	const tiled = 0x0200000000000001
	o, k, _ := testOutput(t)
	o.primary.formats = []ports.DMABufFormat{{Format: fourccXRGB, Modifier: tiled}, {Format: fourccXRGB, Modifier: 0}, {Format: fourccXR30, Modifier: tiled}}
	nv12 := uint32('N' | 'V'<<8 | '1'<<16 | '2'<<24)
	for _, tc := range []struct {
		name   string
		format uint32
		mod    uint64
		as     uint32
		ok     bool
	}{
		{"argb as xrgb", fourccARGB, tiled, fourccXRGB, true},
		{"xrgb linear", fourccXRGB, 0, fourccXRGB, true},
		{"10 bit alpha as opaque", fourccAR30, tiled, fourccXR30, true},
		{"modifier not listed", fourccXRGB, 0x0200000000000002, 0, false},
		{"format not listed", nv12, tiled, 0, false},
	} {
		got, ok := o.scanoutFormat(&ports.DMABuf{Format: tc.format, Modifier: tc.mod})
		if ok != tc.ok || ok && got != tc.as {
			t.Errorf("%s: %#x %v", tc.name, got, ok)
		}
	}
	sampled := []ports.DMABufFormat{{Format: fourccARGB, Modifier: tiled}, {Format: fourccARGB, Modifier: 0x0200000000000002}, {Format: nv12, Modifier: tiled}}
	if got := o.scanoutFormats(sampled); len(got) != 1 || got[0] != sampled[0] {
		t.Fatalf("tranche %v", got)
	}
	// A buffer not in IN_FORMATS is refused before any import.
	o.clientFBs = map[uint64]*clientFB{}
	if fb, reason := o.scanoutFB(&ports.DMABuf{ID: 4, Format: nv12, Modifier: tiled}, time.Now()); fb != 0 || reason != "format" {
		t.Fatalf("fb %d reason %q", fb, reason)
	}
	// A listed one is imported with every plane (addFB builds ADDFB2).
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(88, nil).Once()
	if fb, reason := o.scanoutFB(&ports.DMABuf{ID: 5, Format: fourccARGB, Modifier: tiled, Planes: []ports.DMABufPlane{{}, {}}}, time.Now()); fb != 88 || reason != "" {
		t.Fatalf("fb %d reason %q", fb, reason)
	}
}

// Each modeset reports the formats clients may allocate for scanout;
// with direct scanout off the list is empty.
func TestModesetSendsScanoutFormats(t *testing.T) {
	const tiled = 0x0200000000000001
	for _, on := range []bool{true, false} {
		o, k, _ := testOutput(t)
		o.cursor = nil
		o.scanout, o.device = on, 9
		o.primary.formats = []ports.DMABufFormat{{Format: fourccXRGB, Modifier: tiled}, {Format: fourccNV12, Modifier: tiled}, {Format: fourccP010, Modifier: tiled}}
		o.sampled = []ports.DMABufFormat{{Format: fourccARGB, Modifier: tiled}, {Format: fourccARGB, Modifier: 0}, {Format: fourccNV12, Modifier: tiled}, {Format: fourccP010, Modifier: tiled}}
		ch := make(chan ports.OutputFormats, 1)
		o.formats = ch
		k.EXPECT().createBlob(mock.Anything).Return(99, nil).Once()
		if err := o.modeset(); err != nil {
			t.Fatal(err)
		}
		f := <-ch
		if f.Output != "DP-1" || f.Device != 9 {
			t.Fatalf("report %+v", f)
		}
		if on != (len(f.Formats) == 1 && f.Formats[0] == o.sampled[0]) || !on && len(f.Formats) != 0 {
			t.Fatalf("scanout=%v formats %v", on, f.Formats)
		}
	}
}

func TestHDRScanoutDecision(t *testing.T) {
	o, k, _ := testOutput(t)
	o.hdrOn = true
	o.primary.formats = []ports.DMABufFormat{{Format: fourccXR30}}
	scene := ports.Scene{OutputWidth: 200, OutputHeight: 100, Windows: []ports.SceneWindow{{ID: 1, Fullscreen: true, Rect: ports.Rect{W: 200, H: 100}}}}
	c := ports.SurfaceContent{ID: 1, Width: 200, Height: 100, LogicalW: 200, LogicalH: 100, DMABuf: &ports.DMABuf{ID: 11, Format: fourccXR30}}
	check := func(want string) {
		t.Helper()
		fb, _ := o.scanoutFrame(scene, map[ports.WindowID]ports.SurfaceContent{1: c})
		if o.reason != want || (fb != 0) != (want == "") {
			t.Fatalf("fb %d reason %q, want %q", fb, o.reason, want)
		}
	}
	check("hdr_sdr_content")
	c.Color = ports.SurfaceColor{TF: ports.ColorTFPQ, Primaries: ports.ColorPrimariesBT2020}
	c.DMABuf.Format = fourccXRGB
	check("hdr_format")
	c.DMABuf.Format = fourccP010
	check("yuv")
	c.DMABuf.Format = fourccXR30
	k.EXPECT().addFB(mock.Anything, uint32(fourccXR30)).Return(uint32(77), nil).Once()
	check("")
	o.hdrOn = false
	check("sdr_pq_content")
}

func TestModesetReportsConfirmedHDR(t *testing.T) {
	o, k, _ := testOutput(t)
	o.cursor = nil
	o.hdrOn = true
	o.hdr = hdrCapability{MaxLuminance: 1000, MaxFrameAverage: 400, MinLuminance: 0.005}
	o.primary.formats = []ports.DMABufFormat{{Format: fourccXR30}, {Format: fourccXRGB}}
	o.sampled = []ports.DMABufFormat{{Format: fourccXR30}, {Format: fourccXRGB}}
	ch := make(chan ports.OutputFormats, 2)
	o.formats = ch
	k.EXPECT().createBlob(mock.Anything).Return(99, nil).Twice()
	k.EXPECT().destroyBlob(uint32(99)).Return(nil).Once()
	if err := o.modeset(); err != nil {
		t.Fatal(err)
	}
	if f := <-ch; f.HDR == nil || f.HDR.MaxLuminance != 1000 || f.HDR.MaxFrameAverage != 400 || f.HDR.MinLuminance != 0.005 || len(f.Formats) != 1 || f.Formats[0].Format != fourccXR30 {
		t.Fatalf("HDR report: %+v", f)
	}
	o.hdrOn = false // fallback to SDR
	if err := o.modeset(); err != nil {
		t.Fatal(err)
	}
	if f := <-ch; f.HDR != nil || len(f.Formats) != 1 || f.Formats[0].Format != fourccXRGB {
		t.Fatalf("SDR fallback report: %+v", f)
	}
}

// Power off withdraws HDR and scanout formats: clients must not target an
// inactive output.
func TestPowerOffWithdrawsHDRAndFormats(t *testing.T) {
	o, _, _ := testOutput(t)
	o.cursor = nil
	o.hdrOn, o.scanout = true, true
	o.hdr = hdrCapability{MaxLuminance: 1000}
	o.primary.formats = []ports.DMABufFormat{{Format: fourccXR30}}
	o.sampled = []ports.DMABufFormat{{Format: fourccXR30}}
	ch := make(chan ports.OutputFormats, 1)
	o.formats = ch
	if err := o.powerOff(); err != nil {
		t.Fatal(err)
	}
	select {
	case f := <-ch:
		if f.HDR != nil || len(f.Formats) != 0 {
			t.Fatalf("power-off report: %+v", f)
		}
	default:
		t.Fatal("no report after power off")
	}
}

// A plane without IN_FORMATS (driver without modifiers) scans out its
// GETPLANE formats, linear.
func TestPlaneWithoutInFormatsIsLinear(t *testing.T) {
	k := newMockkms(t)
	k.EXPECT().planes().Return([]planeRes{{id: 1, possible: 1, formats: []uint32{fourccXRGB}}}, nil)
	k.EXPECT().objProps(uint32(1), uint32(objPlane)).Return(map[string][2]uint64{"type": {20, planePrimary}}, nil)
	ps, err := readPlanes(k, 0)
	if err != nil || len(ps) != 1 {
		t.Fatalf("%v %v", ps, err)
	}
	o := &Output{primary: ps[0]}
	if f, ok := o.scanoutFormat(&ports.DMABuf{Format: fourccARGB}); !ok || f != fourccXRGB {
		t.Fatalf("linear argb: %#x %v", f, ok)
	}
	if _, ok := o.scanoutFormat(&ports.DMABuf{Format: fourccARGB, Modifier: 0x0200000000000001}); ok {
		t.Fatal("tiled accepted without IN_FORMATS")
	}
}

// A flip shows only the windows its scene draws: presentation feedback of
// a window on another workspace, hidden or scrolled off the output is
// discarded, not presented.
func TestShownByScene(t *testing.T) {
	s := ports.Scene{OutputWidth: 10, OutputHeight: 10, Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{X: 5, W: 10, H: 10}}, {ID: 2, Hidden: true}, {ID: 3, Rect: ports.Rect{X: 10, W: 10, H: 10}}}, Layers: []ports.SceneLayer{{ID: 5}}}
	got := (&Output{}).shownBy(s, map[ports.WindowID]uint64{1: 3, 2: 4, 3: 8, 5: 6, 9: 7})
	if len(got) != 2 || got[1] != 3 || got[5] != 6 {
		t.Fatalf("shows %v", got)
	}
}

// The decision is callable without Run and keeps the zero-allocation
// composition and cached direct-scanout paths cheap.
func TestFrameDecisionAllocations(t *testing.T) {
	o, _, _ := testOutput(t)
	o.scanout = true
	s := ports.Scene{OutputWidth: 200, OutputHeight: 100, Windows: []ports.SceneWindow{{ID: 1, Fullscreen: true, Rect: ports.Rect{W: 200, H: 100}}}}
	c := ports.SurfaceContent{ID: 1, Width: 200, Height: 100, LogicalW: 200, LogicalH: 100, DMABuf: &ports.DMABuf{ID: 17, Format: fourccXRGB}}
	surfaces := map[ports.WindowID]ports.SurfaceContent{1: c}
	o.clientFBs[17] = &clientFB{fbID: 77}
	o.primary.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	for _, tc := range []struct {
		name       string
		capture    bool
		wantFB     uint32
		wantReason string
	}{
		{"direct", false, 77, ""}, {"capture", true, 0, "capture"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := o.decideFrame(s, surfaces, tc.capture)
			if d.fb != tc.wantFB || o.reason != tc.wantReason {
				t.Fatalf("decision: fb=%d reason=%q", d.fb, o.reason)
			}
			if tc.capture && o.overlayReason != "capture" {
				t.Fatalf("capture overlay reason %q", o.overlayReason)
			}
			if n := testing.AllocsPerRun(100, func() { o.decideFrame(s, surfaces, tc.capture) }); n != 0 {
				t.Fatalf("decision: %.1f allocs", n)
			}
		})
	}
	o.primary.formats = nil
	d := o.decideFrame(s, surfaces, false)
	if d.fb != 0 || o.overlayReason != "no_plane" || len(d.composed.Windows) != 1 {
		t.Fatalf("composition: %+v", d)
	}
	if n := testing.AllocsPerRun(100, func() { o.decideFrame(s, surfaces, false) }); n != 0 {
		t.Fatalf("composition decision: %.1f allocs", n)
	}
}

func TestFrameDecisionOverlayFallback(t *testing.T) {
	o, k, _ := overlayOutput(t)
	o.overlay.formats = []ports.DMABufFormat{{Format: fourccXRGB}}
	s, surfaces := overlayScene()
	k.EXPECT().addFB(mock.Anything, uint32(fourccXRGB)).Return(88, nil).Once()
	d := o.decideFrame(s, surfaces, false)
	if d.fb != 0 || d.overlay.fb != 88 || o.reason != "other_windows" || len(d.composed.Windows) != 1 {
		t.Fatalf("overlay decision: %+v", d)
	}
	d.overlay.close()
	o.clientFBs[9].overlayFailed = "overlay_refused"
	d = o.decideFrame(s, surfaces, false)
	if d.overlay.fb != 0 || o.overlayReason != "overlay_refused" || len(d.composed.Windows) != 2 {
		t.Fatalf("fallback decision: %+v", d)
	}
}
