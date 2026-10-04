package vulkan

import (
	"bytes"
	"image"
	"image/color"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/neferwl/internal/ports"
)

// TestSceneFieldCount fails when Scene gains or loses a field: sceneDelta
// lists the fields that force a full redraw.
func TestSceneFieldCount(t *testing.T) {
	const fields = 20
	assert.Equal(t, fields, len(reflect.VisibleFields(reflect.TypeOf(ports.Scene{}))),
		"Scene fields changed: check sceneDelta (a field it does not compare must force a full redraw or be covered), then this count")
}

// TestSceneWindowFieldCount fails when SceneWindow gains or loses a field:
// sceneDelta compares windows by struct equality, so a new field is covered
// as long as it only changes where or how that window draws (inside Rect).
func TestSceneWindowFieldCount(t *testing.T) {
	const fields = 15
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

// The paint order splits at the first window that opens the floats: when
// that window changes, the whole output is redrawn.
func TestSceneDeltaFloatsStartMoves(t *testing.T) {
	phys := func(r ports.Rect) image.Rectangle { return image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H) }
	tile := func(id ports.WindowID, x int) ports.SceneWindow { return win(id, x, 0, 100, 100) }
	float := func(id ports.WindowID, x int) ports.SceneWindow {
		w := win(id, x, 0, 100, 100)
		w.Floating = true
		return w
	}
	delta := func(a, b []ports.SceneWindow) bool {
		_, ok := sceneDelta(deltaScene(1, a...), deltaScene(2, b...), phys, 1<<30)
		return ok
	}
	// A tile becomes a float: the tile lines and the veil move under it.
	assert.False(t, delta([]ports.SceneWindow{tile(1, 0), tile(2, 100)}, []ports.SceneWindow{tile(1, 0), float(2, 100)}))
	// The first float moves in the list.
	assert.False(t, delta([]ports.SceneWindow{float(1, 0), float(2, 100)}, []ports.SceneWindow{tile(1, 0), float(2, 100)}))
	// A hidden or popup float opens nothing; showing it does.
	hidden := float(2, 100)
	hidden.Hidden = true
	assert.False(t, delta([]ports.SceneWindow{tile(1, 0), hidden}, []ports.SceneWindow{tile(1, 0), float(2, 100)}))
	popup := float(2, 100)
	popup.Popup = true
	assert.False(t, delta([]ports.SceneWindow{tile(1, 0), popup}, []ports.SceneWindow{tile(1, 0), float(2, 100)}))
	// A below float or an overview preview does not open the floats.
	below := float(2, 100)
	below.Below = true
	assert.True(t, delta([]ports.SceneWindow{tile(1, 0), tile(2, 100)}, []ports.SceneWindow{tile(1, 0), below}))
	preview := float(2, 100)
	preview.Preview = 0.5
	assert.True(t, delta([]ports.SceneWindow{tile(1, 0), tile(2, 100)}, []ports.SceneWindow{tile(1, 0), preview}))
	// The same first float with another rect stays a delta.
	assert.True(t, delta([]ports.SceneWindow{tile(1, 0), float(2, 100)}, []ports.SceneWindow{tile(1, 0), float(2, 130)}))
}

// The target keeps its own copy of the compared slices: a caller reusing
// its slices in place (DRM overlay frames, capture) must not change what the
// target holds.
func TestTargetHoldCopiesSlices(t *testing.T) {
	ws := []ports.SceneWindow{win(1, 0, 0, 100, 100), win(2, 100, 0, 100, 100)}
	seps := []ports.Separator{{Rect: ports.Rect{X: 100, W: 2, H: 100}}}
	layers := []ports.SceneLayer{{ID: 9, Rect: ports.Rect{W: 10, H: 10}}}
	hints := []ports.Rect{{W: 5, H: 5}}
	marks := []ports.CaptureIndicator{{Rect: ports.Rect{W: 5, H: 5}}}
	s := deltaScene(1)
	s.Windows, s.Separators, s.Layers, s.DropHints, s.CaptureIndicators = ws, seps, layers, hints, marks
	tg := &target{}
	d := &damageRegion{drawn: map[ports.WindowID]heldWindow{}}
	tg.hold(s, d)
	ws[1].Rect.X, seps[0].Rect.X, layers[0].ID, hints[0].W, marks[0].Rect.W = 150, 150, 8, 6, 6
	phys := func(r ports.Rect) image.Rectangle { return image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H) }
	// Compared with the mutated slices as the new scene, the held copy
	// differs in each; with a held reference nothing would differ.
	cur := s
	cur.Seq = 2
	_, ok := sceneDelta(tg.scene, cur, phys, 1<<30)
	assert.False(t, ok, "layers, hints and indicators changed: full redraw")
	cur.Layers, cur.DropHints, cur.CaptureIndicators = tg.heldLayers, tg.heldHints, tg.heldIndicators
	area, ok := sceneDelta(tg.scene, cur, phys, 1<<30)
	require.True(t, ok)
	assert.Equal(t, image.Rect(100, 0, 250, 100), area, "window 2, old and new")
	// Zero allocations once the buffers are warm.
	allocs := testing.AllocsPerRun(50, func() { tg.hold(s, d) })
	assert.Zero(t, allocs, "hold must not allocate once warm")
}

// deltaCase is a frame sequence of two windows whose second one resizes.
type deltaCase struct {
	name      string
	scale     float64
	out       ports.BufferTransform
	sceneDim  float64
	floating  bool // window 2 is a float: the veil and tile lines go under it
	windowDim bool // window 1 is dimmed on some frames
}

func (c deltaCase) scene(seq uint64, i, w2 int, windows []ports.SceneWindow, seps []ports.Separator) ports.Scene {
	windows[0], windows[1] = win(1, 0, 0, 20, 48), win(2, 20, 0, w2, 48)
	windows[1].Floating = c.floating
	if c.windowDim && i%3 == 0 {
		windows[0].Dim = 0.5
	}
	seps[0] = ports.Separator{Rect: ports.Rect{X: 20, W: 1, H: 48}}
	return ports.Scene{Seq: seq, Transform: c.out, OutputWidth: 64, OutputHeight: 48, Scale: c.scale, Background: "#101010",
		Dim: c.sceneDim, Windows: windows, Separators: seps}
}

func deltaCases() []deltaCase {
	return []deltaCase{
		{name: "scale 1", scale: 1},
		{name: "scale 1.5", scale: 1.5},
		{name: "transform 90", scale: 1, out: 1},
		{name: "scene dim over tiles", scale: 1, sceneDim: 0.4, floating: true},
		{name: "window dim", scale: 1, windowDim: true},
		{name: "scale 1.5 rotated, all", scale: 1.5, out: 1, sceneDim: 0.3, floating: true, windowDim: true},
	}
}

// A partial redraw after a window resize is pixel-identical to a full
// redraw, on both targets of a double-buffered output. With reuse the scene
// slices are rewritten in place for every frame, like an output adapter
// that reuses them: the target must not have kept a reference.
func TestRendererSceneDeltaMatchesFullRedraw(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		for _, tc := range deltaCases() {
			name := tc.name
			if reuse {
				name += " reused slices"
			}
			t.Run(name, func(t *testing.T) { sceneDeltaMatchesFullRedraw(t, tc, reuse) })
		}
	}
}

func sceneDeltaMatchesFullRedraw(t *testing.T, tc deltaCase, reuse bool) {
	tw, th := tc.out.Size(int(64*tc.scale+0.5), int(48*tc.scale+0.5))
	newR := func() *Renderer {
		r, err := New(tw, th)
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
	shared := make([]ports.SceneWindow, 2)
	sharedSeps := make([]ports.Separator, 1)
	var partial int
	for i, w2 := range []int{20, 20, 30, 24, 24, 40, 20, 22} {
		windows, seps := shared, sharedSeps
		if !reuse {
			windows, seps = make([]ports.SceneWindow, 2), make([]ports.Separator, 1)
		}
		if reuse {
			clear(windows)
		}
		s := tc.scene(uint64(10+i), i, w2, windows, seps)
		damaged.UseTarget(i % 2)
		before := damaged.redrawn
		require.NoError(t, render(damaged, s, contents))
		if damaged.redrawn-before < tw*th {
			partial++
		}
		plain := s
		plain.Seq = 0
		plain.Windows = slices.Clone(s.Windows)
		plain.Separators = slices.Clone(s.Separators)
		require.NoError(t, render(full, plain, contents))
		a, b := readPixels(t, damaged), readPixels(t, full)
		if !bytes.Equal(a.Pix, b.Pix) {
			t.Fatalf("frame %d (w2=%d) differs from a full redraw", i, w2)
		}
	}
	assert.Positive(t, partial, "no frame was redrawn partially")
}

// A window's Fade changes only how it draws inside its rect: the delta is
// that rect. A leaving entry (core keeps a closed or hidden window in the
// scene while it fades) changes the window list when it appears and when
// it goes: both redraw everything, like any list change; while it fades,
// only its rect is redrawn.
func TestDamageDeltaFadeAndLeavingEntry(t *testing.T) {
	bounds := image.Rect(0, 0, 1000, 500)
	settled := deltaScene(1, win(1, 0, 0, 500, 500), win(2, 500, 0, 500, 500))

	faded := deltaScene(2, win(1, 0, 0, 500, 500), win(2, 500, 0, 500, 500))
	faded.Windows[1].Fade = 0.5
	d := newDamage(held(settled), faded, bounds)
	require.False(t, d.all())
	assert.Equal(t, image.Rect(500, 0, 1000, 500), d.area)

	// Window 2 was unmapped: the layout re-flows and a leaving entry is
	// appended. The list changed: full redraw.
	leaving := deltaScene(3, win(1, 0, 0, 1000, 500), win(2, 500, 0, 500, 500))
	leaving.Windows[1].Fade = 0.1
	assert.True(t, newDamage(held(settled), leaving, bounds).all())

	// The fade advances: its rect only, the neighbours untouched.
	next := deltaScene(4, win(1, 0, 0, 1000, 500), win(2, 500, 0, 500, 500))
	next.Windows[1].Fade = 0.6
	d = newDamage(held(leaving), next, bounds)
	require.False(t, d.all())
	assert.Equal(t, image.Rect(500, 0, 1000, 500), d.area)

	// The entry settled and is dropped: full redraw (its pixels are stale).
	gone := deltaScene(5, win(1, 0, 0, 1000, 500))
	assert.True(t, newDamage(held(next), gone, bounds).all())

	// A leaving float opens the floats where it is appended: the tile
	// lines and the veil move under it, so floatsStart differs.
	float := deltaScene(6, win(1, 0, 0, 1000, 500), win(2, 100, 100, 200, 200))
	float.Windows[1].Floating, float.Windows[1].Fade = true, 0.1
	assert.True(t, newDamage(held(gone), float, bounds).all())
}
