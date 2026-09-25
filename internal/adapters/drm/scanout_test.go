package drm

import (
	"testing"

	"github.com/bnema/nefertty/internal/ports"
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
		{"unsupported format", nil, func(c *ports.SurfaceContent) {
			d := *c.DMABuf
			d.Format = 'N' | 'V'<<8 | '1'<<16 | '2'<<24
			c.DMABuf = &d
		}, "format"},
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

func TestScanoutFormat(t *testing.T) {
	for f, want := range map[uint32]bool{fourccXRGB: true, fourccARGB: true, 'A' | 'B'<<8 | '2'<<16 | '4'<<24: false} {
		got, ok := scanoutFormat(f)
		if ok != want || ok && got != fourccXRGB {
			t.Errorf("%x: %x %v", f, got, ok)
		}
	}
}
