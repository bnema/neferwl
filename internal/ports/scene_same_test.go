package ports

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

func sameSceneBase() Scene {
	return Scene{
		Security:     SecurityState{Generation: 1},
		Output:       "A",
		Seq:          1,
		OutputWidth:  100,
		OutputHeight: 50,
		Scale:        1,
		Background:   "#000000",
		Border:       Border{Width: 2, Active: "#ffffff", Inactive: "#222222"},
		Windows:      []SceneWindow{{ID: 1, Rect: Rect{X: 1, W: 10, H: 10}}},
		Separators:   []Separator{{Rect: Rect{W: 1, H: 10}}},
		DropHints:    []Rect{{W: 5, H: 5}},
		Layers:       []SceneLayer{{ID: 9, Rect: Rect{W: 100, H: 5}}},
		Capture:      &SceneCapture{Shown: 1, Excluded: []WindowID{3}},

		CaptureIndicators: []CaptureIndicator{{Rect: Rect{W: 4, H: 4}}},
	}
}

func TestSceneSameAs(t *testing.T) {
	base := sameSceneBase()

	same := sameSceneBase()
	same.Seq = 99
	assert.True(t, base.SameAs(same), "Seq alone must not matter")
	assert.True(t, Scene{}.SameAs(Scene{Seq: 3}))

	for name, edit := range map[string]func(*Scene){
		"window rect":       func(s *Scene) { s.Windows[0].Rect.X++ },
		"window fade":       func(s *Scene) { s.Windows[0].Fade = 0.5 },
		"window zoom":       func(s *Scene) { s.Windows[0].Zoom = 0.9 },
		"window count":      func(s *Scene) { s.Windows = nil },
		"security":          func(s *Scene) { s.Security.Generation++ },
		"background":        func(s *Scene) { s.Background = "#111111" },
		"border":            func(s *Scene) { s.Border.Width++ },
		"output":            func(s *Scene) { s.Output = "B" },
		"size":              func(s *Scene) { s.OutputWidth++ },
		"scale":             func(s *Scene) { s.Scale = 2 },
		"off":               func(s *Scene) { s.Off = true },
		"clip":              func(s *Scene) { s.WorkspaceClip.W = 1 },
		"tile clip":         func(s *Scene) { s.TileClip.H = 1 },
		"dim":               func(s *Scene) { s.Dim = 0.5 },
		"dim behind":        func(s *Scene) { s.DimBehind = true },
		"separator":         func(s *Scene) { s.Separators[0].Active = true },
		"drop hint":         func(s *Scene) { s.DropHints[0].W++ },
		"layer":             func(s *Scene) { s.Layers[0].Rect.H++ },
		"indicator":         func(s *Scene) { s.CaptureIndicators[0].Pill = true },
		"excluded":          func(s *Scene) { s.Capture.Excluded[0] = 4 },
		"transform":         func(s *Scene) { s.Transform = 1 },
		"capture shown":     func(s *Scene) { s.Capture.Shown++ },
		"capture session":   func(s *Scene) { s.Capture.Session++ },
		"capture workspace": func(s *Scene) { s.Capture.Workspace++ },
		"capture window":    func(s *Scene) { s.Capture.Window++ },
		"capture revision":  func(s *Scene) { s.Capture.Revision++ },
		"capture nil":       func(s *Scene) { s.Capture = nil },
		"capture scene":     func(s *Scene) { s.CaptureScene = &Scene{} },
	} {
		t.Run(name, func(t *testing.T) {
			o := sameSceneBase()
			edit(&o)
			assert.False(t, base.SameAs(o))
			assert.False(t, o.SameAs(base))
		})
	}

	both := sameSceneBase()
	both.CaptureScene = &Scene{}
	other := sameSceneBase()
	other.CaptureScene = &Scene{}
	assert.False(t, both.SameAs(other), "a capture scene is never the same")
}

// TestSceneSameAsCoversEveryField fails when Scene gains or loses a field:
// update Scene.SameAs, then this count.
func TestSceneSameAsCoversEveryField(t *testing.T) {
	const fields = 21
	assert.Equal(t, fields, len(reflect.VisibleFields(reflect.TypeOf(Scene{}))),
		"Scene fields changed: update Scene.SameAs (and TestSceneSameAs), then this count")
}

// TestSceneCaptureSameAsCoversEveryField fails when SceneCapture gains or
// loses a field: update Scene.SameAs, then this count.
func TestSceneCaptureSameAsCoversEveryField(t *testing.T) {
	const fields = 6
	assert.Equal(t, fields, len(reflect.VisibleFields(reflect.TypeOf(SceneCapture{}))),
		"SceneCapture fields changed: update Scene.SameAs (and TestSceneSameAs), then this count")
}
