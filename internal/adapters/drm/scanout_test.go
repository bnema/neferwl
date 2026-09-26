package drm

import (
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/ports"
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
		{"top layer covered", func(s *ports.Scene) {
			s.Layers = []ports.SceneLayer{{ID: 3, Layer: ports.LayerTop, Rect: ports.Rect{W: 10, H: 10}}}
		}, nil, ""},
		{"shm", nil, func(c *ports.SurfaceContent) { c.DMABuf, c.SHM = nil, &ports.SHMBuffer{} }, "not_dmabuf"},
		{"subsurface", nil, func(c *ports.SurfaceContent) { c.Children = []ports.Subsurface{{}} }, "subsurfaces"},
		{"scaled", nil, func(c *ports.SurfaceContent) { c.Width = 100 }, "size_mismatch"},
		{"cropped", nil, func(c *ports.SurfaceContent) { c.Geometry = ports.Rect{X: 5, W: 90, H: 50} }, "geometry_crop"},
		{"client ignores scale", nil, func(c *ports.SurfaceContent) { c.LogicalW = 200 }, "logical_mismatch"},
		{"whole geometry", nil, func(c *ports.SurfaceContent) { c.Geometry = ports.Rect{W: 100, H: 50} }, ""},
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
		o.primary.formats = []ports.DMABufFormat{{Format: fourccXRGB, Modifier: tiled}}
		o.sampled = []ports.DMABufFormat{{Format: fourccARGB, Modifier: tiled}, {Format: fourccARGB, Modifier: 0}}
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
