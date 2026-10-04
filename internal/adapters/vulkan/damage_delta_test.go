package vulkan

import (
	"bytes"
	"image"
	"image/color"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/neferwl/internal/ports"
)

// TestSceneWindowFieldCount fails when SceneWindow gains or loses a field:
// sceneDelta compares windows by struct equality, so a new field is covered
// as long as it only changes where or how that window draws (inside Rect).
func TestSceneWindowFieldCount(t *testing.T) {
	const fields = 13
	assert.Equal(t, fields, len(reflect.VisibleFields(reflect.TypeOf(ports.SceneWindow{}))),
		"SceneWindow fields changed: check sceneDelta (a field that draws outside Rect needs its own rule), then this count")
}

func deltaScene(seq uint64, ws ...ports.SceneWindow) ports.Scene {
	return ports.Scene{Seq: seq, OutputWidth: 1000, OutputHeight: 500, Scale: 1, Background: "#000000", Windows: ws}
}

func win(id ports.WindowID, x, y, w, h int) ports.SceneWindow {
	return ports.SceneWindow{ID: id, Rect: ports.Rect{X: x, Y: y, W: w, H: h}}
}

// held is a target that holds s after a full frame of its windows.
func held(s ports.Scene) *target {
	tg := &target{}
	tg.hold(s, &damageRegion{drawn: map[ports.WindowID]heldWindow{}})
	return tg
}

func TestDamageOneWindowResizeIsUnionOfRects(t *testing.T) {
	bounds := image.Rect(0, 0, 1000, 500)
	old := deltaScene(1, win(1, 0, 0, 300, 500), win(2, 300, 0, 300, 500), win(3, 600, 0, 400, 500))
	cur := deltaScene(2, win(1, 0, 0, 300, 500), win(2, 300, 0, 300, 500), win(3, 600, 0, 380, 400))
	d := newDamage(held(old), cur, bounds)
	require.False(t, d.all())
	assert.Equal(t, image.Rect(600, 0, 1000, 500), d.area)

	// Window moves: both places.
	cur = deltaScene(2, win(1, 0, 0, 300, 500), win(2, 310, 0, 300, 500), win(3, 600, 0, 400, 500))
	d = newDamage(held(old), cur, bounds)
	require.False(t, d.all())
	assert.Equal(t, image.Rect(300, 0, 610, 500), d.area)
}

func TestDamageDeltaAtScale(t *testing.T) {
	old := deltaScene(1, win(1, 0, 0, 10, 10), win(2, 100, 0, 10, 10))
	cur := deltaScene(2, win(1, 0, 0, 10, 10), win(2, 100, 0, 20, 10))
	old.Scale, cur.Scale = 1.5, 1.5
	d := newDamage(held(old), cur, image.Rect(0, 0, 1500, 750))
	require.False(t, d.all())
	assert.Equal(t, image.Rect(150, 0, 180, 15), d.area)
}

func TestDamageDeltaWindowFieldChange(t *testing.T) {
	old := deltaScene(1, win(1, 0, 0, 100, 100), win(2, 100, 0, 100, 100))
	cur := deltaScene(2, win(1, 0, 0, 100, 100), win(2, 100, 0, 100, 100))
	cur.Windows[0].FocusEffect = 0.3
	d := newDamage(held(old), cur, image.Rect(0, 0, 1000, 500))
	require.False(t, d.all())
	assert.Equal(t, image.Rect(0, 0, 100, 100), d.area)
}

func TestDamageDeltaFullRedraws(t *testing.T) {
	base := func() ports.Scene {
		return deltaScene(1, win(1, 0, 0, 100, 100), win(2, 100, 0, 100, 100))
	}
	bounds := image.Rect(0, 0, 1000, 500)
	for name, mod := range map[string]func(*ports.Scene){
		"camera shift over half": func(s *ports.Scene) {
			s.Windows = []ports.SceneWindow{win(1, 0, 0, 600, 500), win(2, 600, 0, 400, 500)}
			s.Windows[0].Rect.X = 50
		},
		"layer":          func(s *ports.Scene) { s.Layers = []ports.SceneLayer{{ID: 9, Rect: ports.Rect{W: 10, H: 10}}} },
		"output":         func(s *ports.Scene) { s.Output = "DP-2" },
		"size":           func(s *ports.Scene) { s.OutputWidth = 999 },
		"scale":          func(s *ports.Scene) { s.Scale = 2 },
		"transform":      func(s *ports.Scene) { s.Transform = 1 },
		"off":            func(s *ports.Scene) { s.Off = true },
		"background":     func(s *ports.Scene) { s.Background = "#111111" },
		"border":         func(s *ports.Scene) { s.Border.Width = 2 },
		"workspace clip": func(s *ports.Scene) { s.WorkspaceClip = ports.Rect{W: 10, H: 10} },
		"dim":            func(s *ports.Scene) { s.Dim = 0.5 },
		"dim behind":     func(s *ports.Scene) { s.DimBehind = true },
		"drop hints":     func(s *ports.Scene) { s.DropHints = []ports.Rect{{W: 5, H: 5}} },
		"indicators":     func(s *ports.Scene) { s.CaptureIndicators = []ports.CaptureIndicator{{Rect: ports.Rect{W: 5, H: 5}}} },
		"capture":        func(s *ports.Scene) { s.Capture = &ports.SceneCapture{} },
		"capture scene":  func(s *ports.Scene) { s.CaptureScene = &ports.Scene{} },
		"security":       func(s *ports.Scene) { s.Security.Protected = true },
		"window count":   func(s *ports.Scene) { s.Windows = s.Windows[:1] },
		"window order":   func(s *ports.Scene) { s.Windows[0], s.Windows[1] = s.Windows[1], s.Windows[0] },
		"window id":      func(s *ports.Scene) { s.Windows[1].ID = 7 },
	} {
		t.Run(name, func(t *testing.T) {
			cur := base()
			mod(&cur)
			cur.Seq = 2
			assert.True(t, newDamage(held(base()), cur, bounds).all())
		})
	}
	t.Run("seq 0 and cold target", func(t *testing.T) {
		cur := base()
		cur.Seq = 0
		assert.True(t, newDamage(held(base()), cur, bounds).all())
		cur.Seq = 2
		assert.True(t, newDamage(&target{}, cur, bounds).all())
	})
}

func TestDamageDeltaSeparators(t *testing.T) {
	sep := func(x int) ports.Separator { return ports.Separator{Rect: ports.Rect{X: x, W: 2, H: 500}} }
	a, b := sep(100), sep(200)
	old := deltaScene(1, win(1, 0, 0, 500, 500))
	old.Separators = []ports.Separator{a, b}
	cur := deltaScene(2, win(1, 0, 0, 500, 500))
	cur.Separators = []ports.Separator{a, sep(250)}
	d := newDamage(held(old), cur, image.Rect(0, 0, 1000, 500))
	require.False(t, d.all())
	assert.Equal(t, image.Rect(200, 0, 252, 500), d.area)

	// Same lines, another order: nothing differs by value.
	cur.Separators = []ports.Separator{b, a}
	d = newDamage(held(old), cur, image.Rect(0, 0, 1000, 500))
	require.False(t, d.all())
	assert.True(t, d.area.Empty())

	// A line that turns active is a different value.
	cur.Separators = []ports.Separator{a, {Rect: b.Rect, Active: true}}
	d = newDamage(held(old), cur, image.Rect(0, 0, 1000, 500))
	require.False(t, d.all())
	assert.Equal(t, image.Rect(200, 0, 202, 500), d.area)
}

func TestSceneDeltaAllocations(t *testing.T) {
	old := deltaScene(1, win(1, 0, 0, 300, 500), win(2, 300, 0, 300, 500), win(3, 600, 0, 400, 500))
	cur := deltaScene(2, win(1, 0, 0, 300, 500), win(2, 300, 0, 300, 500), win(3, 600, 0, 380, 500))
	for i := range 6 {
		s := ports.Separator{Rect: ports.Rect{X: i * 100, W: 2, H: 500}}
		old.Separators = append(old.Separators, s)
		cur.Separators = append(cur.Separators, s)
	}
	cur.Separators[5].Active = true
	phys := func(r ports.Rect) image.Rectangle { return image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H) }
	var area image.Rectangle
	var ok bool
	allocs := testing.AllocsPerRun(50, func() { area, ok = sceneDelta(old, cur, phys, 450000) })
	require.True(t, ok)
	assert.False(t, area.Empty())
	assert.Zero(t, allocs, "sceneDelta must not allocate")
}

// A partial redraw after a window resize is pixel-identical to a full
// redraw, on both targets of a double-buffered output.
func TestRendererSceneDeltaMatchesFullRedraw(t *testing.T) {
	newR := func() *Renderer {
		r, err := New(64, 48)
		if err != nil {
			t.Skipf("Vulkan unavailable: %v", err)
		}
		t.Cleanup(r.Close)
		return r
	}
	damaged, full := newR(), newR()
	bufs, err := damaged.ExportTargets(2, nil, false)
	if err != nil {
		t.Skipf("no exportable targets: %v", err)
	}
	for _, b := range bufs {
		for _, p := range b.Planes {
			p.File.Close()
		}
	}
	red := solidContent(t, 8, 8, color.RGBA{R: 255, A: 255})
	red.ID, red.Surface, red.Seq, red.Version = 1, 1, 1, 1
	green := solidContent(t, 8, 8, color.RGBA{G: 255, A: 255})
	green.ID, green.Surface, green.Seq, green.Version = 2, 2, 1, 1
	contents := map[ports.WindowID]ports.SurfaceContent{1: red, 2: green}
	mk := func(seq uint64, w2 int) ports.Scene {
		s := ports.Scene{Seq: seq, OutputWidth: 64, OutputHeight: 48, Scale: 1, Background: "#101010",
			Windows: []ports.SceneWindow{win(1, 0, 0, 20, 48), win(2, 20, 0, w2, 48)}}
		s.Separators = []ports.Separator{{Rect: ports.Rect{X: 20, W: 1, H: 48}}}
		return s
	}
	var partial int
	for i, w2 := range []int{20, 20, 30, 24, 24, 40, 20} {
		s := mk(uint64(10+i), w2)
		damaged.UseTarget(i % 2)
		before := damaged.redrawn
		require.NoError(t, render(damaged, s, contents))
		if damaged.redrawn-before < 64*48 {
			partial++
		}
		plain := s
		plain.Seq = 0
		require.NoError(t, render(full, plain, contents))
		a, b := readPixels(t, damaged), readPixels(t, full)
		if !bytes.Equal(a.Pix, b.Pix) {
			t.Fatalf("frame %d (w2=%d) differs from a full redraw", i, w2)
		}
	}
	assert.Positive(t, partial, "no frame was redrawn partially")
}
