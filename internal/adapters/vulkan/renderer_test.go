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
		if err := r.Render(s); err != nil {
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
	s := ports.Scene{Background: "#102030", Windows: []ports.SceneWindow{win}}
	check(s, map[image.Point]color.RGBA{{0, 0}: bg, {16, 16}: wc})
	s.Windows[0].Focused = true
	check(s, map[image.Point]color.RGBA{{9, 9}: {255, 255, 255, 255}, {16, 16}: wc})
	s.Windows[0] = ports.SceneWindow{ID: 2, Rect: ports.Rect{X: -10, Y: 0, W: 20, H: 10}}
	c = windowColor(2)
	check(s, map[image.Point]color.RGBA{{5, 5}: {c[0], c[1], c[2], 255}, {15, 5}: bg})
	s.Windows[0].Hidden = true
	check(s, map[image.Point]color.RGBA{{5, 5}: bg})
}
