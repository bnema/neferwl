package vulkan

import (
	"image"
	"image/color"
	"os"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"golang.org/x/sys/unix"
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
	wc, c2, fc := color.RGBA{200, 10, 10, 255}, color.RGBA{10, 200, 10, 255}, color.RGBA{10, 10, 200, 255}
	contents := map[ports.WindowID]ports.SurfaceContent{
		1: solidContent(t, 64, 48, wc), 2: solidContent(t, 64, 48, c2), 3: solidContent(t, 64, 48, fc),
	}
	check := func(s ports.Scene, expected map[image.Point]color.RGBA) {
		t.Helper()
		if err := render(r, s, contents); err != nil {
			t.Fatal(err)
		}
		for p, want := range expected {
			if got := r.Pixels().At(p.X, p.Y); got != want {
				t.Errorf("pixel %v=%v want %v", p, got, want)
			}
		}
	}
	// The client sits inside its inset sides; separators are drawn over
	// the windows in order, with the border colors.
	win := ports.SceneWindow{ID: 1, Rect: ports.Rect{X: 8, Y: 8, W: 16, H: 16}, Inset: ports.SideAll}
	gray, lit := color.RGBA{0x31, 0x32, 0x44, 255}, color.RGBA{0xb4, 0xbe, 0xfe, 255}
	s := ports.Scene{Background: "#102030", Border: ports.Border{Width: 2, Active: "#b4befe", Inactive: "#313244"}, Windows: []ports.SceneWindow{win}}
	check(s, map[image.Point]color.RGBA{{0, 0}: bg, {9, 9}: bg, {10, 10}: wc, {16, 16}: wc, {21, 21}: wc, {22, 22}: bg, {24, 24}: bg})
	s.Separators = []ports.Separator{
		{Rect: ports.Rect{X: 8, Y: 8, W: 16, H: 2}},
		{Rect: ports.Rect{X: 8, Y: 8, W: 8, H: 2}, Active: true},
	}
	check(s, map[image.Point]color.RGBA{{9, 9}: lit, {15, 9}: lit, {16, 9}: gray, {23, 9}: gray, {10, 10}: wc})
	s.Border.Inactive = ""
	check(s, map[image.Point]color.RGBA{{9, 9}: lit, {16, 9}: bg})
	// A float covers the tile lines; its own border is drawn with it.
	s.Windows = []ports.SceneWindow{win, {ID: 3, Rect: ports.Rect{X: 12, Y: 4, W: 8, H: 8}, Floating: true, Inset: ports.SideAll}}
	s.Separators = []ports.Separator{
		{Rect: ports.Rect{X: 8, Y: 8, W: 16, H: 2}, Active: true},
		{Rect: ports.Rect{X: 12, Y: 4, W: 8, H: 1}, Active: true, Window: 3},
	}
	check(s, map[image.Point]color.RGBA{{9, 9}: lit, {14, 9}: fc, {14, 4}: lit})
	s.Windows = []ports.SceneWindow{win}
	s.Separators, s.Border.Inactive = nil, "#313244"
	s.Windows[0].Fullscreen = true
	check(s, map[image.Point]color.RGBA{{9, 9}: wc})
	s.Windows[0].Fullscreen = false
	s.Windows[0] = ports.SceneWindow{ID: 2, Rect: ports.Rect{X: -10, Y: 0, W: 20, H: 10}}
	check(s, map[image.Point]color.RGBA{{5, 5}: c2, {15, 5}: bg})
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
		if err := render(r, scene, contents); err != nil {
			t.Fatal(err)
		}
	}
	check := func(x, y int, want color.RGBA) {
		t.Helper()
		if got := r.Pixels().At(x, y); got != want {
			t.Errorf("At(%d,%d)=%v want %v", x, y, got, want)
		}
	}
	render(shmContent(t, 4, 2, 16, pixels))
	check(8, 8, color.RGBA{255, 0, 0, 255})
	check(11, 9, color.RGBA{255, 0, 0, 255})
	check(12, 8, bg)
	check(0, 0, bg)
	scene.Windows[0].Rect = ports.Rect{X: -2, Y: 0, W: 10, H: 10}
	padded := make([]byte, 4*20)
	copy(padded[8:12], []byte{7, 11, 23, 255})
	render(shmContent(t, 4, 4, 20, padded))
	check(0, 0, color.RGBA{23, 11, 7, 255})
	// A window without a buffer yet shows the background, not a color.
	render(nil)
	check(0, 0, bg)
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
	top := color.RGBA{10, 200, 10, 255}
	upper := map[ports.WindowID]ports.SurfaceContent{1: solidContent(t, 16, 16, color.RGBA{200, 10, 10, 255}), 2: solidContent(t, 16, 16, top)}
	for id, c := range upper {
		c.Version = 1 // the opaque buffer below is another commit
		upper[id] = c
	}
	if err := render(r, scene, upper); err != nil {
		t.Fatal(err)
	}
	if got, want := r.Pixels().RGBAAt(10, 10), top; got != want {
		t.Errorf("overlap = %v, want %v", got, want)
	}
	scene.Border = ports.Border{Width: 4, Active: "#ffffff"}
	scene.Windows = []ports.SceneWindow{{ID: 1, Rect: ports.Rect{X: 2, Y: 2, W: 16, H: 16}, Focused: true, Inset: ports.SideAll}}
	scene.Separators = []ports.Separator{{Rect: ports.Rect{X: 2, Y: 2, W: 16, H: 4}, Active: true}}
	px := make([]byte, 16*16*4)
	for i := 0; i < len(px); i += 4 {
		copy(px[i:i+4], []byte{3, 5, 7, 0})
	}
	opaque := shmContent(t, 16, 16, 64, px)
	opaque.Opaque = true
	contents := map[ports.WindowID]ports.SurfaceContent{1: *opaque}
	if err := render(r, scene, contents); err != nil {
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
	layer := shmContent(t, 64, 8, 64*4, red)
	layer.ID, layer.Opaque = 2, true
	contents := map[ports.WindowID]ports.SurfaceContent{2: *layer}
	scene := ports.Scene{Background: "#102030", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 48}}}, Layers: []ports.SceneLayer{{ID: 2, Layer: ports.LayerTop, Rect: ports.Rect{W: 64, H: 8}}}}
	window := color.RGBA{16, 32, 48, 255} // no buffer yet: background
	check := func(want color.RGBA) {
		t.Helper()
		if err := render(r, scene, contents); err != nil {
			t.Fatal(err)
		}
		if got := r.Pixels().RGBAAt(5, 2); got != want {
			t.Errorf("pixel = %v, want %v", got, want)
		}
	}
	check(color.RGBA{255, 0, 0, 255})
	scene.Layers[0].Layer = ports.LayerBottom
	check(window)
	// Core leaves hidden layers out of the scene; the renderer draws what
	// it gets, over a fullscreen window too.
	scene.Windows[0].Fullscreen = true
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
	s := ports.Scene{Scale: 2, Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{X: 4, Y: 4, W: 10, H: 10}}}}
	scaled := shmContent(t, 20, 20, 80, solid(20, 20, red))
	scaled.ID, scaled.LogicalW, scaled.LogicalH = 1, 10, 10
	content := map[ports.WindowID]ports.SurfaceContent{1: *scaled}
	if err := render(r, s, content); err != nil {
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
	if err := render(r, s, content); err != nil {
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
	odd := shmContent(t, 16, 15, 64, solid(16, 15, red))
	odd.ID, odd.LogicalW, odd.LogicalH = 1, 10, 10
	content[1] = *odd
	if err := render(r, s, content); err != nil {
		t.Fatal(err)
	}
	if got := r.Pixels().At(21, 10); got != (color.RGBA{0, 0, 0, 255}) {
		t.Errorf("clipped column = %v", got)
	}
}

// solidContent is a w×h B8G8R8A8 buffer of one color.
func solidContent(t *testing.T, w, h int, c color.RGBA) ports.SurfaceContent {
	px := make([]byte, w*h*4)
	for i := 0; i < len(px); i += 4 {
		px[i], px[i+1], px[i+2], px[i+3] = c.B, c.G, c.R, 255
	}
	return *shmContent(t, w, h, w*4, px)
}

// shmContent puts pixels in a memfd pool, like a wl_shm client.
func shmContent(t *testing.T, w, h, stride int, pixels []byte) *ports.SurfaceContent {
	t.Helper()
	fd, err := unix.MemfdCreate("shm-test", unix.MFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(fd), "shm-test")
	t.Cleanup(func() { f.Close() })
	if _, err := f.Write(pixels); err != nil {
		t.Fatal(err)
	}
	shmPools++
	return &ports.SurfaceContent{Width: w, Height: h, SHM: &ports.SHMBuffer{Pool: shmPools, File: f, Stride: stride}}
}

// shmPools numbers test pools: a renderer maps each pool ID once.
var shmPools uint64

// The window geometry lands on the window rect and the client shadow
// around it is clipped; subsurfaces draw from the root origin, above or
// below it.
func TestRendererGeometryAndSubsurfaces(t *testing.T) {
	r, err := New(64, 48)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	red, green, blue := color.RGBA{255, 0, 0, 255}, color.RGBA{0, 255, 0, 255}, color.RGBA{0, 0, 255, 255}
	bg := color.RGBA{16, 32, 48, 255}
	scene := ports.Scene{Background: "#102030", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{X: 10, Y: 10, W: 20, H: 10}}}}
	// A 30×20 surface whose window is the 20×10 at (5, 5): a 5px shadow.
	root := solidContent(t, 30, 20, red)
	root.Surface, root.Version = 1, 1
	root.Geometry = ports.Rect{X: 5, Y: 5, W: 20, H: 10}
	// Green covers the window's top-left 4×4 corner; blue sits below the
	// root, hidden by it.
	below := solidContent(t, 30, 20, blue)
	below.Surface, below.Version = 2, 1
	above := solidContent(t, 4, 4, green)
	above.Surface, above.Version = 3, 1
	root.Children = []ports.Subsurface{
		{X: 3, Y: 3, Below: true, SurfaceContent: below},
		{X: 5, Y: 5, SurfaceContent: above},
	}
	if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: root}); err != nil {
		t.Fatal(err)
	}
	px := r.Pixels()
	for _, c := range []struct {
		x, y int
		want color.RGBA
	}{
		{10, 10, green}, {13, 13, green}, {14, 14, red}, {29, 19, red},
		// The shadow and the child reaching past it are clipped.
		{9, 10, bg}, {30, 19, bg}, {29, 20, bg},
	} {
		if got := px.At(c.x, c.y); got != c.want {
			t.Errorf("At(%d,%d)=%v want %v", c.x, c.y, got, c.want)
		}
	}
	// A root with no buffer of its own still shows its children.
	only := ports.SurfaceContent{Children: []ports.Subsurface{{SurfaceContent: solidContent(t, 20, 10, blue)}}}
	if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: only}); err != nil {
		t.Fatal(err)
	}
	if got := r.Pixels().At(20, 15); got != blue {
		t.Errorf("child only: %v", got)
	}
}

// A tall fill covers every row through the last one.
func TestRendererTallFill(t *testing.T) {
	h := 135
	r, err := New(8, h)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	scene := ports.Scene{Background: "#102030", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{X: 1, Y: 0, W: 6, H: h}}}}
	wc := color.RGBA{200, 10, 10, 255}
	if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: solidContent(t, 6, h, wc)}); err != nil {
		t.Fatal(err)
	}
	bg := color.RGBA{16, 32, 48, 255}
	px := r.Pixels()
	for y := 0; y < h; y++ {
		if got := px.At(1, y); got != wc {
			t.Fatalf("At(1,%d)=%v want %v", y, got, wc)
		}
		if got := px.At(6, y); got != wc {
			t.Fatalf("At(6,%d)=%v want %v", y, got, wc)
		}
		if got := px.At(7, y); got != bg {
			t.Fatalf("At(7,%d)=%v want background", y, got)
		}
	}
}

// fill returns a w×h premultiplied B8G8R8A8 buffer of one pixel.
func fill(w, h int, bgra [4]byte) []byte {
	p := make([]byte, w*h*4)
	for i := 0; i < len(p); i += 4 {
		copy(p[i:i+4], bgra[:])
	}
	return p
}

// Translucent buffers blend over what is below them, in paint order: a
// bottom layer (one draw), an opaque window copied over it, then two
// overlapping overlays sharing one draw. A transparent pixel changes nothing.
func TestRendererAlphaBlend(t *testing.T) {
	r, err := New(64, 48)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	halfRed, halfBlue := [4]byte{0, 0, 128, 128}, [4]byte{128, 0, 0, 128}
	partly := fill(16, 8, halfRed)
	for y := range 8 {
		copy(partly[y*64+48:y*64+64], make([]byte, 16)) // last 4 pixels: transparent
	}
	white := shmContent(t, 32, 48, 32*4, solid(32, 48, [3]uint8{255, 255, 255}))
	white.Opaque = true
	contents := map[ports.WindowID]ports.SurfaceContent{
		1: *white,
		2: *shmContent(t, 64, 48, 64*4, fill(64, 48, halfRed)),
		3: *shmContent(t, 16, 8, 16*4, partly),
		4: *shmContent(t, 16, 8, 16*4, fill(16, 8, halfBlue)),
	}
	scene := ports.Scene{Background: "#000000",
		Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 32, H: 48}}},
		Layers: []ports.SceneLayer{
			{ID: 2, Layer: ports.LayerBottom, Rect: ports.Rect{W: 64, H: 48}},
			{ID: 3, Layer: ports.LayerOverlay, Rect: ports.Rect{W: 16, H: 8}},
			{ID: 4, Layer: ports.LayerOverlay, Rect: ports.Rect{X: 8, W: 16, H: 8}},
		}}
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	px := r.Pixels()
	near := func(a, b uint8) bool { d := int(a) - int(b); return d >= -1 && d <= 1 }
	for _, tc := range []struct {
		name    string
		x, y    int
		r, g, b uint8
	}{
		{"bottom layer over background", 40, 20, 128, 0, 0},
		{"opaque window over bottom layer", 20, 20, 255, 255, 255},
		{"red overlay over window", 4, 4, 255, 127, 127},
		{"blue over red over window", 10, 4, 127, 63, 191},
		{"blue over window", 20, 4, 127, 127, 255},
		{"transparent red pixels: blue over window only", 14, 4, 127, 127, 255},
	} {
		got := px.RGBAAt(tc.x, tc.y)
		if !near(got.R, tc.r) || !near(got.G, tc.g) || !near(got.B, tc.b) {
			t.Errorf("%s: pixel (%d,%d) = %v, want ~{%d %d %d}", tc.name, tc.x, tc.y, got, tc.r, tc.g, tc.b)
		}
	}
}
