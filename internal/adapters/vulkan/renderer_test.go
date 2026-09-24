package vulkan

import (
	"github.com/bnema/nefertty/internal/ports"
	"image"
	"image/color"
	"testing"
)

func TestRendererClear(t *testing.T) {
	r, err := New(64, 48)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	if err := r.Clear([3]uint8{0x10, 0x20, 0x30}); err != nil {
		t.Fatal(err)
	}
	pixels := r.Pixels()
	want := color.RGBA{0x10, 0x20, 0x30, 255}
	for _, p := range [][2]int{{0, 0}, {63, 47}} {
		if got := pixels.At(p[0], p[1]); got != want {
			t.Errorf("pixel %v = %v, want %v", p, got, want)
		}
	}
}

func TestParseColor(t *testing.T) {
	for _, tc := range []struct {
		s    string
		want [3]uint8
	}{{"#102030", [3]uint8{16, 32, 48}}, {"#FFFFFF", [3]uint8{255, 255, 255}}, {"bad", [3]uint8{}}, {"#12gg34", [3]uint8{}}, {"102030", [3]uint8{}}} {
		if got := parseColor(tc.s); got != tc.want {
			t.Errorf("parseColor(%q)=%v want %v", tc.s, got, tc.want)
		}
	}
}

func TestRendererRender(t *testing.T) {
	r, err := New(64, 48)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	bg := color.RGBA{16, 32, 48, 255}
	check := func(s ports.Scene, expected map[image.Point]color.RGBA) {
		t.Helper()
		if err := r.Render(s, nil); err != nil {
			t.Fatal(err)
		}
		for p, want := range expected {
			if got := r.Pixels().At(p.X, p.Y); got != want {
				t.Errorf("pixel %v=%v want %v", p, got, want)
			}
		}
	}
	win := ports.SceneWindow{ID: 1, Rect: ports.Rect{X: 8, Y: 8, W: 16, H: 16}}
	c := windowColor(1)
	wc := color.RGBA{c[0], c[1], c[2], 255}
	s := ports.Scene{Background: "#102030", Border: ports.Border{Width: 2, Active: "#b4befe", Inactive: "#313244"}, Windows: []ports.SceneWindow{win}}
	check(s, map[image.Point]color.RGBA{{0, 0}: bg, {9, 9}: {0x31, 0x32, 0x44, 255}, {10, 10}: wc, {16, 16}: wc})
	s.Windows[0].Focused = true
	check(s, map[image.Point]color.RGBA{{9, 9}: {0xb4, 0xbe, 0xfe, 255}, {10, 10}: wc, {16, 16}: wc})
	s.Windows[0].Fullscreen = true
	check(s, map[image.Point]color.RGBA{{9, 9}: wc})
	s.Windows[0].Fullscreen = false
	s.Windows[0] = ports.SceneWindow{ID: 2, Rect: ports.Rect{X: -10, Y: 0, W: 20, H: 10}}
	c = windowColor(2)
	check(s, map[image.Point]color.RGBA{{5, 5}: {c[0], c[1], c[2], 255}, {15, 5}: bg})
	s.Windows[0].Hidden = true
	check(s, map[image.Point]color.RGBA{{5, 5}: bg})
}

func TestRendererContents(t *testing.T) {
	r, err := New(64, 48)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	bg := color.RGBA{16, 32, 48, 255}
	scene := ports.Scene{Background: "#102030", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{X: 8, Y: 8, W: 20, H: 10}}}}
	pixels := make([]byte, 4*2*4)
	for i := 0; i < len(pixels); i += 4 {
		copy(pixels[i:i+4], []byte{0, 0, 255, 255})
	}
	render := func(c *ports.SurfaceContent) {
		t.Helper()
		var contents map[ports.WindowID]ports.SurfaceContent
		if c != nil {
			contents = map[ports.WindowID]ports.SurfaceContent{1: *c}
		}
		if err := r.Render(scene, contents); err != nil {
			t.Fatal(err)
		}
	}
	check := func(x, y int, want color.RGBA) {
		t.Helper()
		if got := r.Pixels().At(x, y); got != want {
			t.Errorf("At(%d,%d)=%v want %v", x, y, got, want)
		}
	}
	render(&ports.SurfaceContent{Width: 4, Height: 2, Stride: 16, Pixels: pixels})
	check(8, 8, color.RGBA{255, 0, 0, 255})
	check(11, 9, color.RGBA{255, 0, 0, 255})
	check(12, 8, bg)
	check(0, 0, bg)
	scene.Windows[0].Rect = ports.Rect{X: -2, Y: 0, W: 10, H: 10}
	padded := make([]byte, 4*20)
	copy(padded[8:12], []byte{7, 11, 23, 255})
	render(&ports.SurfaceContent{Width: 4, Height: 4, Stride: 20, Pixels: padded})
	check(0, 0, color.RGBA{23, 11, 7, 255})
	render(nil)
	c := windowColor(1)
	check(0, 0, color.RGBA{c[0], c[1], c[2], 255})
}

func TestRendererUploadOrderAndOpaque(t *testing.T) {
	r, err := New(32, 32)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	scene := ports.Scene{Background: "#102030", Windows: []ports.SceneWindow{
		{ID: 1, Rect: ports.Rect{X: 2, Y: 2, W: 16, H: 16}},
		{ID: 2, Rect: ports.Rect{X: 8, Y: 8, W: 16, H: 16}},
	}}
	if err := r.Render(scene, nil); err != nil {
		t.Fatal(err)
	}
	c := windowColor(2)
	if got, want := r.Pixels().RGBAAt(10, 10), (color.RGBA{c[0], c[1], c[2], 255}); got != want {
		t.Errorf("overlap = %v, want %v", got, want)
	}
	scene.Border = ports.Border{Width: 4, Active: "#ffffff"}
	scene.Windows = []ports.SceneWindow{{ID: 1, Rect: ports.Rect{X: 2, Y: 2, W: 16, H: 16}, Focused: true}}
	contents := map[ports.WindowID]ports.SurfaceContent{1: {Width: 16, Height: 16, Stride: 64, Opaque: true, Pixels: make([]byte, 16*16*4)}}
	for i := 0; i < len(contents[1].Pixels); i += 4 {
		copy(contents[1].Pixels[i:i+4], []byte{3, 5, 7, 0})
	}
	if err := r.Render(scene, contents); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		x, y int
		want color.RGBA
	}{{3, 3, color.RGBA{255, 255, 255, 255}}, {10, 10, color.RGBA{7, 5, 3, 255}}} {
		if got := r.Pixels().RGBAAt(tc.x, tc.y); got != tc.want {
			t.Errorf("pixel (%d,%d) = %v, want %v", tc.x, tc.y, got, tc.want)
		}
	}
}

func TestRendererLayers(t *testing.T) {
	r, err := New(64, 48)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	red := make([]byte, 64*8*4)
	for i := 0; i < len(red); i += 4 {
		copy(red[i:i+4], []byte{0, 0, 255, 0})
	}
	contents := map[ports.WindowID]ports.SurfaceContent{2: {ID: 2, Width: 64, Height: 8, Stride: 64 * 4, Opaque: true, Pixels: red}}
	scene := ports.Scene{Background: "#102030", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 48}}}, Layers: []ports.SceneLayer{{ID: 2, Layer: ports.LayerTop, Rect: ports.Rect{W: 64, H: 8}}}}
	wc := windowColor(1)
	window := color.RGBA{wc[0], wc[1], wc[2], 255}
	check := func(want color.RGBA) {
		t.Helper()
		if err := r.Render(scene, contents); err != nil {
			t.Fatal(err)
		}
		if got := r.Pixels().RGBAAt(5, 2); got != want {
			t.Errorf("pixel = %v, want %v", got, want)
		}
	}
	check(color.RGBA{255, 0, 0, 255})
	scene.Layers[0].Layer = ports.LayerBottom
	check(window)
	scene.Windows[0].Fullscreen = true
	scene.Layers[0].Layer = ports.LayerTop
	check(window)
	scene.Layers[0].Layer = ports.LayerOverlay
	check(color.RGBA{255, 0, 0, 255})
	scene.Windows[0].Hidden = true
	scene.Layers[0].Layer = ports.LayerBottom
	check(color.RGBA{255, 0, 0, 255})
	delete(contents, 2)
	check(color.RGBA{16, 32, 48, 255})
}

// solid returns a w×h B8G8R8A8 buffer of one color.
func solid(w, h int, c [3]uint8) []byte {
	p := make([]byte, w*h*4)
	for i := 0; i < len(p); i += 4 {
		p[i], p[i+1], p[i+2], p[i+3] = c[2], c[1], c[0], 255
	}
	return p
}

func TestRendererScale(t *testing.T) {
	r, err := New(64, 48)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	red := [3]uint8{200, 0, 0}
	// Scale 2: a 10x10 logical window at (4,4) covers physical (8,8)-(28,28);
	// its 20x20 buffer (fractional client) is copied 1:1.
	s := ports.Scene{Scale: 2, Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{X: 4, Y: 4, W: 10, H: 10}, Borderless: true}}}
	content := map[ports.WindowID]ports.SurfaceContent{1: {ID: 1, Width: 20, Height: 20, LogicalW: 10, LogicalH: 10, Stride: 80, Pixels: solid(20, 20, red)}}
	if err := r.Render(s, content); err != nil {
		t.Fatal(err)
	}
	px := r.Pixels()
	want := color.RGBA{200, 0, 0, 255}
	for _, p := range []image.Point{{8, 8}, {27, 27}} {
		if got := px.At(p.X, p.Y); got != want {
			t.Errorf("%v = %v", p, got)
		}
	}
	for _, p := range []image.Point{{7, 7}, {28, 28}} {
		if got := px.At(p.X, p.Y); got != (color.RGBA{0, 0, 0, 255}) {
			t.Errorf("outside %v = %v", p, got)
		}
	}
	// Scale 1.5 with an integer-scale client (buffer scale 2): the 20x20
	// buffer is 10x10 logical and downscaled to 15x15 physical at (6,6).
	s.Scale = 1.5
	if err := r.Render(s, content); err != nil {
		t.Fatal(err)
	}
	px = r.Pixels()
	for _, p := range []image.Point{{6, 6}, {20, 20}} {
		if got := px.At(p.X, p.Y); got != want {
			t.Errorf("1.5 %v = %v", p, got)
		}
	}
	if got := px.At(21, 21); got != (color.RGBA{0, 0, 0, 255}) {
		t.Errorf("1.5 outside = %v", got)
	}
	// A buffer 1px wider than its slot is clipped, not resampled.
	content[1] = ports.SurfaceContent{ID: 1, Width: 16, Height: 15, LogicalW: 10, LogicalH: 10, Stride: 64, Pixels: solid(16, 15, red)}
	if err := r.Render(s, content); err != nil {
		t.Fatal(err)
	}
	if got := r.Pixels().At(21, 10); got != (color.RGBA{0, 0, 0, 255}) {
		t.Errorf("clipped column = %v", got)
	}
}
