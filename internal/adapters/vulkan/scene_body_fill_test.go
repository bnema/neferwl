package vulkan

import (
	"image"
	"testing"
	"unsafe"

	"github.com/bnema/neferwl/internal/domain"
)

// Cached SHM needs no Vulkan calls: these tests inspect the actual emitted
// draws, including the absence of content when its pool cannot be read.
func TestSceneBodyFillCovered(t *testing.T) {
	for _, tc := range []struct {
		name                string
		opaque              bool
		width               int
		fade, zoom, preview float64
		radius              int
		unreadable, solid   bool
		wantFill            bool
	}{
		{name: "opaque", opaque: true, width: 40},
		{name: "translucent", width: 40, wantFill: true},
		{name: "smaller", opaque: true, width: 20, wantFill: true},
		{name: "faded", opaque: true, width: 40, fade: .5, wantFill: true},
		{name: "rounded", opaque: true, width: 40, radius: 5, wantFill: true},
		{name: "zoomed", opaque: true, width: 40, zoom: .5, wantFill: true},
		{name: "preview", opaque: true, width: 40, preview: .5, wantFill: true},
		{name: "unreadable pool", opaque: true, width: 40, unreadable: true, wantFill: true},
		{name: "solid content", opaque: true, width: 40, solid: true, wantFill: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := domain.SurfaceContent{ID: 1, Surface: 1, Seq: 1, Version: 1, Width: tc.width, Height: 30, Opaque: tc.opaque, SHM: &domain.SHMBuffer{Stride: tc.width * 4}}
			cached := &shmCopy{valid: true, holds: shmState{w: tc.width, h: 30, seq: 1}}
			r := &Renderer{width: 80, height: 60, shm: map[shmKey]*shmSurface{{win: 1, surface: 1}: {bufs: [2]*shmCopy{cached}}}}
			if tc.unreadable {
				pixels := make([]byte, tc.width*30*4)
				cached.valid = false
				cached.gpu = gpuBuffer{mapped: unsafe.Pointer(&pixels[0]), size: len(pixels)}
				r.gpu = &GPU{maxRange: len(pixels)}
				r.shm[shmKey{win: 1, surface: 1}].bufs[1] = cached
			}
			if tc.solid {
				content.SHM = nil
				content.Solid = &domain.SolidColor{R: 1, A: 1}
			}
			s := domain.Scene{Scale: 1, Background: "#123456", Windows: []domain.SceneWindow{{ID: 1, Rect: domain.Rect{W: 40, H: 30}, Fade: tc.fade, Zoom: tc.zoom, Preview: tc.preview, Radius: tc.radius}}}
			ds := r.draws(s, map[domain.WindowID]domain.SurfaceContent{1: content}, newDamage(&target{}, s, image.Rect(0, 0, 80, 60)))
			fills := 0
			for _, d := range ds {
				if d.pc.misc[0] == modeSolid && d.pc.color[0] != 1 {
					fills++
				}
			}
			want := 0
			if tc.wantFill {
				want = 1
			}
			if fills != want {
				t.Fatalf("body fills = %d, want %d (draws: %+v)", fills, want, ds)
			}
			if !tc.unreadable && !tc.solid && len(drawRects(ds)) != 1 {
				t.Fatalf("content draw missing: %+v", ds)
			}
		})
	}
}
