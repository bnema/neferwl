package drm

import (
	"slices"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// frontImage is what the output image last committed by a composed frame
// holds: the scene drawn into it and the content Seq of each window and
// layer it lists. When only the window on the overlay plane changes, the
// next frame commits that image again with the new overlay buffer: no GPU
// pass, no fence. Paths that draw into an output image outside
// submitFrame (security, protection, image setup, the async probe) forget
// it.
type frontImage struct {
	fb    uint32 // 0: unknown
	scene ports.Scene
	seqs  []uint64 // Seq of each window, then each layer, of scene
	// Backing arrays of scene's slices, reused across frames.
	windows    []ports.SceneWindow
	separators []ports.Separator
	layers     []ports.SceneLayer
	hints      []domain.Rect
	indicators []ports.CaptureIndicator
}

// forget drops the image's contents: the next frame composes.
func (f *frontImage) forget() { f.fb = 0 }

// hold records that fb holds s drawn from surfaces. A capture scene is not
// recorded (SameAs never matches it).
func (f *frontImage) hold(fb uint32, s ports.Scene, surfaces map[domain.WindowID]domain.SurfaceContent) {
	if s.Capture != nil || s.CaptureScene != nil {
		f.forget()
		return
	}
	f.windows = append(f.windows[:0], s.Windows...)
	f.separators = append(f.separators[:0], s.Separators...)
	f.layers = append(f.layers[:0], s.Layers...)
	f.hints = append(f.hints[:0], s.DropHints...)
	f.indicators = append(f.indicators[:0], s.CaptureIndicators...)
	f.scene = s
	f.scene.Windows, f.scene.Separators, f.scene.Layers = f.windows, f.separators, f.layers
	f.scene.DropHints, f.scene.CaptureIndicators = f.hints, f.indicators
	f.seqs = appendSeqs(f.seqs[:0], s, surfaces)
	f.fb = fb
}

// holds reports whether fb holds exactly what drawing s from surfaces would.
func (f *frontImage) holds(fb uint32, s ports.Scene, surfaces map[domain.WindowID]domain.SurfaceContent) bool {
	if f.fb == 0 || f.fb != fb || !s.SameAs(f.scene) {
		return false
	}
	i := 0
	for _, w := range s.Windows {
		if surfaces[w.ID].Seq != f.seqs[i] {
			return false
		}
		i++
	}
	for _, l := range s.Layers {
		if surfaces[l.ID].Seq != f.seqs[i] {
			return false
		}
		i++
	}
	return true
}

func appendSeqs(seqs []uint64, s ports.Scene, surfaces map[domain.WindowID]domain.SurfaceContent) []uint64 {
	seqs = slices.Grow(seqs, len(s.Windows)+len(s.Layers))
	for _, w := range s.Windows {
		seqs = append(seqs, surfaces[w.ID].Seq)
	}
	for _, l := range s.Layers {
		seqs = append(seqs, surfaces[l.ID].Seq)
	}
	return seqs
}
