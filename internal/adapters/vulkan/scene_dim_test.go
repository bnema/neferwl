package vulkan

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// These walk tests need no Vulkan device: placeholder windows and solid
// separators produce draw commands without importing client buffers.
func TestSceneDimOrder(t *testing.T) {
	r := &Renderer{width: 80, height: 60}
	s := ports.Scene{
		Dim: 0.4, Border: ports.Border{Active: "#ffffff"},
		Windows: []ports.SceneWindow{
			{ID: 1, Rect: ports.Rect{W: 20, H: 20}},
			{ID: 2, Floating: true, Hidden: true, Rect: ports.Rect{W: 20, H: 20}},
			{ID: 3, Floating: true, Popup: true, Rect: ports.Rect{W: 20, H: 20}},
			{ID: 4, Floating: true, Rect: ports.Rect{W: 0, H: 20}},
			{ID: 5, Floating: true, Rect: ports.Rect{X: 30, W: 20, H: 20}},
			{ID: 6, Floating: true, Rect: ports.Rect{X: 50, W: 20, H: 20}},
		},
		Separators: []ports.Separator{{Rect: ports.Rect{W: 80, H: 1}, Active: true}},
	}
	walk := func(s ports.Scene) []draw {
		return r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 80, 60)))
	}
	got := walk(s)
	if len(got) != 5 { // tile, tile lines, veil, two floats
		t.Fatalf("draws = %d, want 5", len(got))
	}
	veil := got[2]
	if veil.pc.rect != [4]int32{0, 0, 80, 60} || veil.pc.color != [4]float32{0, 0, 0, float32(s.Dim)} || veil.pc.misc[0] != modeSolid || veil.pc.misc[1] != 0 {
		t.Fatalf("veil = %+v", veil.pc)
	}
	if got[1].pc.color != [4]float32{1, 1, 1, 1} || got[3].pc.rect[0] != 30 || got[4].pc.rect[0] != 50 {
		t.Fatalf("tile line / float order: %+v", got)
	}
	for _, tc := range []struct {
		name      string
		dim       float64
		windows   []ports.SceneWindow
		wantCount int
		wantAlpha float32
	}{
		{"zero", 0, s.Windows, 4, 0},
		{"negative", -1, s.Windows, 4, 0},
		{"clamped", 2, s.Windows, 5, 1},
		{"no float", 0.4, s.Windows[:4], 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.Dim, s.Windows = tc.dim, tc.windows
			ds := walk(s)
			if len(ds) != tc.wantCount {
				t.Fatalf("draws = %d, want %d", len(ds), tc.wantCount)
			}
			if tc.wantAlpha != 0 && ds[2].pc.color[3] != tc.wantAlpha {
				t.Fatalf("alpha = %v, want %v", ds[2].pc.color[3], tc.wantAlpha)
			}
		})
	}
}

// A dimmed window gets a veil over its own rect, border included, right
// after it: windows drawn later stay bright.
func TestSceneWindowDim(t *testing.T) {
	r := &Renderer{width: 80, height: 60}
	s := ports.Scene{Border: ports.Border{Width: 1, Inactive: "#ffffff"},
		Windows: []ports.SceneWindow{
			{ID: 1, Floating: true, Dim: 0.5, Inset: ports.SideAll, Rect: ports.Rect{X: -10, W: 20, H: 20}},
			{ID: 2, Floating: true, Inset: ports.SideAll, Rect: ports.Rect{X: 20, W: 20, H: 20}},
		},
		Separators: []ports.Separator{{Window: 1, Rect: ports.Rect{X: -10, W: 20, H: 1}}},
	}
	ds := r.draws(s, nil, newDamage(&target{}, s, image.Rect(0, 0, 80, 60)))
	// Window 1, its border, its veil (clipped to the output), window 2.
	if len(ds) != 4 {
		t.Fatalf("draws = %d, want 4", len(ds))
	}
	if v := ds[2].pc; v.rect != [4]int32{0, 0, 10, 20} || v.color != [4]float32{0, 0, 0, 0.5} {
		t.Fatalf("veil = %+v", v)
	}
	if ds[3].pc.rect[0] != 21 {
		t.Fatalf("window 2 not last: %+v", ds[3].pc)
	}
}

// HDR blends linear light: the dim alpha is raised so reference white
// lands where SDR's encoded dim puts it.
func TestDimAlpha(t *testing.T) {
	if got := dimAlpha(0.5, false); got != 0.5 {
		t.Fatalf("sdr %v", got)
	}
	for _, a := range []float64{0, 0.3, 0.5, 1} {
		got := dimAlpha(a, true)
		if want := 1 - srgbToLinear(1-a); math.Abs(got-want) > 1e-12 || got < a {
			t.Fatalf("hdr %v: %v want %v", a, got, want)
		}
	}
	if dimAlpha(2, true) != 1 || dimAlpha(-1, true) != 0 {
		t.Fatal("not clamped")
	}
}

func TestSceneDimPartialDamage(t *testing.T) {
	r := &Renderer{width: 80, height: 60}
	s := ports.Scene{Seq: 8, Dim: 0.5, Windows: []ports.SceneWindow{
		{ID: 1, Rect: ports.Rect{W: 40, H: 40}},
		{ID: 2, Floating: true, Rect: ports.Rect{X: 50, W: 20, H: 20}},
	}}
	tg := &target{valid: true, sceneSeq: 8, windows: map[ports.WindowID]heldWindow{1: {seq: 1, rect: image.Rect(0, 0, 40, 40)}, 2: {seq: 1, rect: image.Rect(50, 0, 70, 20)}}}
	contents := map[ports.WindowID]ports.SurfaceContent{1: {Seq: 2}, 2: {Seq: 1}}
	dmg := newDamage(tg, s, image.Rect(0, 0, 80, 60))
	ds := r.draws(s, contents, dmg)
	dmg.finish()
	if dmg.all() || dmg.area != image.Rect(0, 0, 40, 40) {
		t.Fatalf("damage = %v (full=%v)", dmg.area, dmg.all())
	}
	ds = dmg.clip(ds)
	if len(ds) != 2 || ds[1].pc.color != [4]float32{0, 0, 0, 0.5} || ds[1].pc.rect != [4]int32{0, 0, 40, 40} {
		t.Fatalf("clipped draws = %+v", ds)
	}
}

// The actual blend must darken already painted pixels without darkening
// floats, window popups or top layers. Also exercise a retained target:
// content damage beneath the veil must replay it over that region.
func TestRendererDimBlendAndDamage(t *testing.T) {
	r, err := New(64, 48)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	s := ports.Scene{Seq: 10, Background: "#ffffff", Dim: 0.5,
		Windows: []ports.SceneWindow{
			{ID: 1, Rect: ports.Rect{W: 20, H: 20}},
			{ID: 2, Floating: true, Rect: ports.Rect{X: 30, W: 10, H: 10}},
			{ID: 3, Popup: true, Rect: ports.Rect{X: 42, W: 8, H: 8}},
		},
		Layers: []ports.SceneLayer{{ID: 4, Layer: ports.LayerTop, Rect: ports.Rect{X: 52, W: 8, H: 8}}},
	}
	white := solidContent(t, 20, 20, color.RGBA{255, 255, 255, 255})
	white.Seq = 1
	popup := solidContent(t, 8, 8, color.RGBA{255, 255, 255, 255})
	top := solidContent(t, 8, 8, color.RGBA{255, 255, 255, 255})
	contents := map[ports.WindowID]ports.SurfaceContent{1: white, 3: popup, 4: top}
	check := func() {
		t.Helper()
		if err := render(r, s, contents); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			x, y int
			want color.RGBA
		}{
			{60, 40, color.RGBA{128, 128, 128, 255}}, // background
			{5, 5, color.RGBA{128, 128, 128, 255}},   // tile
			{43, 5, color.RGBA{255, 255, 255, 255}},  // window popup
			{53, 5, color.RGBA{255, 255, 255, 255}},  // top layer
		} {
			if got := readPixels(t, r).RGBAAt(tc.x, tc.y); !near(got, tc.want, 2) {
				t.Errorf("pixel (%d,%d) = %v, want %v", tc.x, tc.y, got, tc.want)
			}
		}
	}
	check()
	// Reuse the target with unchanged scene Seq, but changed tile content.
	// Red becomes 128 after the black veil, not 255 (missing veil).
	for y := 2; y < 5; y++ {
		for x := 2; x < 5; x++ {
			if _, err := white.SHM.File.WriteAt([]byte{0, 0, 255, 255}, int64((y*20+x)*4)); err != nil {
				t.Fatal(err)
			}
		}
	}
	white.Seq, white.Version = 2, 2
	white.DamageHistory = []ports.SeqDamage{{Seq: 1, Full: true}, {Seq: 2, Rects: []ports.Rect{{X: 2, Y: 2, W: 3, H: 3}}}}
	contents[1] = white
	before := r.redrawn
	if err := render(r, s, contents); err != nil {
		t.Fatal(err)
	}
	if r.redrawn-before >= 64*48 {
		t.Fatal("content change unexpectedly triggered full redraw")
	}
	if got := readPixels(t, r).RGBAAt(3, 3); !near(got, color.RGBA{128, 0, 0, 255}, 2) {
		t.Fatalf("damaged tile beneath veil = %v, want dark red", got)
	}
}

// A per-window veil darkens that window and its border only, also when
// its content alone is redrawn on a retained target.
func TestRendererWindowDimBlendAndDamage(t *testing.T) {
	r, err := New(64, 48)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	s := ports.Scene{Seq: 20, Background: "#ffffff", Border: ports.Border{Width: 1, Inactive: "#ffffff"},
		Windows: []ports.SceneWindow{
			{ID: 1, Floating: true, Dim: 0.5, Inset: ports.SideAll, Rect: ports.Rect{W: 20, H: 20}},
			{ID: 2, Floating: true, Inset: ports.SideAll, Rect: ports.Rect{X: 30, W: 20, H: 20}},
		},
		Separators: []ports.Separator{
			{Window: 1, Rect: ports.Rect{W: 20, H: 1}},
			{Window: 2, Rect: ports.Rect{X: 30, W: 20, H: 1}},
		},
	}
	peek := solidContent(t, 18, 18, color.RGBA{255, 255, 255, 255})
	peek.Seq = 1
	sel := solidContent(t, 18, 18, color.RGBA{255, 255, 255, 255})
	contents := map[ports.WindowID]ports.SurfaceContent{1: peek, 2: sel}
	grey, white := color.RGBA{128, 128, 128, 255}, color.RGBA{255, 255, 255, 255}
	if err := render(r, s, contents); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		x, y int
		want color.RGBA
	}{{5, 5, grey}, {5, 0, grey}, {35, 5, white}, {35, 0, white}, {25, 30, white}} {
		if got := readPixels(t, r).RGBAAt(tc.x, tc.y); !near(got, tc.want, 2) {
			t.Errorf("pixel (%d,%d) = %v, want %v", tc.x, tc.y, got, tc.want)
		}
	}
	// Damage inside the peek replays its veil: red becomes dark red.
	if _, err := peek.SHM.File.WriteAt([]byte{0, 0, 255, 255}, int64((3*18+3)*4)); err != nil {
		t.Fatal(err)
	}
	peek.Seq, peek.Version = 2, 2
	peek.DamageHistory = []ports.SeqDamage{{Seq: 1, Full: true}, {Seq: 2, Rects: []ports.Rect{{X: 3, Y: 3, W: 1, H: 1}}}}
	contents[1] = peek
	if err := render(r, s, contents); err != nil {
		t.Fatal(err)
	}
	if got := readPixels(t, r).RGBAAt(4, 4); !near(got, color.RGBA{128, 0, 0, 255}, 2) {
		t.Fatalf("damaged peek = %v, want dark red", got)
	}
}
