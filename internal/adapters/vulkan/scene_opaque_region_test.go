package vulkan

import (
	"image"
	"image/color"
	"slices"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// opaqueRegionContent is a 2x-scale shm root of w×h logical pixels under one
// child of the same size whose opaque region stops a pixel short of its right
// and bottom edges (Firefox's page over its shm root).
func opaqueRegionContent(t testing.TB, w, h int) ports.SurfaceContent {
	root := solidContent(t, 2*w, 2*h, color.RGBA{R: 255, A: 255})
	root.ID, root.Surface, root.Seq, root.Version = 1, 1, 1, 1
	root.LogicalW, root.LogicalH = w, h
	child := solidContent(t, w, h, color.RGBA{G: 255, A: 255})
	child.Surface, child.Version = 2, 1
	child.LogicalW, child.LogicalH = w, h
	child.OpaqueRect = ports.Rect{W: w - 1, H: h - 1}
	root.Children = []ports.Subsurface{{SurfaceContent: child}}
	return root
}

// drawRects are the rects of the content draws (not the window's fills).
func drawRects(ds []draw) []image.Rectangle {
	var out []image.Rectangle
	for _, d := range ds {
		if d.pc.misc[0] == modeSolid {
			continue
		}
		out = append(out, image.Rect(int(d.pc.rect[0]), int(d.pc.rect[1]), int(d.pc.rect[2]), int(d.pc.rect[3])))
	}
	return out
}

// A child whose opaque region leaves a thin edge out hides the root under the
// region only: the root draws the two strips around it, and the child draws
// over its own whole rect.
func TestSceneOpaqueRegionClipsRoot(t *testing.T) {
	r, err := New(64, 48)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	scene := ports.Scene{Seq: 1, Scale: 1, Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 20, H: 12}}}}
	contents := map[ports.WindowID]ports.SurfaceContent{1: opaqueRegionContent(t, 20, 12)}
	ds := r.draws(scene, contents, newDamage(r.target(), scene, image.Rect(0, 0, 64, 48)))
	want := []image.Rectangle{
		// The root: the strip under the last row, then the one right of the
		// region; then the child over its whole rect.
		image.Rect(0, 11, 20, 12), image.Rect(19, 0, 20, 11),
		image.Rect(0, 0, 20, 12),
	}
	if got := drawRects(ds); !sameRects(got, want) {
		t.Fatalf("draws %v, want %v", got, want)
	}
}

func sameRects(a, b []image.Rectangle) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A wholly opaque child still skips the root.
func TestSceneOpaqueChildSkipsRoot(t *testing.T) {
	r, err := New(64, 48)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	root := opaqueRegionContent(t, 20, 12)
	root.Children[0].Opaque, root.Children[0].OpaqueRect = true, ports.Rect{}
	scene := ports.Scene{Seq: 1, Scale: 1, Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 20, H: 12}}}}
	ds := r.draws(scene, map[ports.WindowID]ports.SurfaceContent{1: root}, newDamage(r.target(), scene, image.Rect(0, 0, 64, 48)))
	if got, want := drawRects(ds), []image.Rectangle{image.Rect(0, 0, 20, 12)}; !sameRects(got, want) {
		t.Fatalf("draws %v, want %v", got, want)
	}
}

// The strips show the root exactly as it draws alone: the child is
// transparent on its last row and column, outside its opaque region, and the
// root's pixels show through there, mapped as without the clipping. Inside
// the region the child shows.
func TestRendererOpaqueRegionStripsKeepRoot(t *testing.T) {
	r, err := New(64, 48)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	const w, h = 20, 12
	scene := ports.Scene{Scale: 1, Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: w, H: h}}}}
	root := opaqueRegionContent(t, w, h)
	// A 2x root with a gradient, to see a wrong strip mapping; a child
	// that is green but for a transparent last row and column.
	px := make([]byte, 2*w*2*h*4)
	for y := range 2 * h {
		for x := range 2 * w {
			copy(px[(y*2*w+x)*4:], []byte{0, uint8(y * 10), uint8(x * 6), 255})
		}
	}
	root.SHM = shmContent(t, 2*w, 2*h, 2*w*4, px).SHM
	cp := make([]byte, w*h*4)
	for y := range h {
		for x := range w {
			if x < w-1 && y < h-1 {
				copy(cp[(y*w+x)*4:], []byte{0, 255, 0, 255})
			}
		}
	}
	root.Children[0].SHM = shmContent(t, w, h, w*4, cp).SHM
	if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: root}); err != nil {
		t.Fatal(err)
	}
	with := readPixels(t, r)
	alone := root
	alone.Children = nil
	alone.Seq, alone.Version = 2, 2
	if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: alone}); err != nil {
		t.Fatal(err)
	}
	without := readPixels(t, r)
	green := color.RGBA{G: 255, A: 255}
	for y := range h {
		for x := range w {
			want := without.At(x, y)
			if x < w-1 && y < h-1 {
				want = green
			}
			if got := with.At(x, y); got != want {
				t.Fatalf("At(%d,%d)=%v want %v", x, y, got, want)
			}
		}
	}
}

// opaqueCover maps the opaque rect onto the physical rect of the surface,
// rounding inward, clipped to what shows.
func TestOpaqueCover(t *testing.T) {
	c := ports.SurfaceContent{LogicalW: 984, LogicalH: 1231, OpaqueRect: ports.Rect{W: 983, H: 1230}}
	for _, tc := range []struct {
		name      string
		full, dst image.Rectangle
		rect      ports.Rect
		want      image.Rectangle
	}{
		{"scale 1.3", image.Rect(0, 0, 1279, 1600), image.Rect(0, 0, 1279, 1600), c.OpaqueRect, image.Rect(0, 0, 1277, 1598)},
		{"offset", image.Rect(10, 20, 1289, 1620), image.Rect(10, 20, 1289, 1620), c.OpaqueRect, image.Rect(10, 20, 1287, 1618)},
		{"clipped", image.Rect(0, 0, 1279, 1600), image.Rect(0, 0, 100, 100), c.OpaqueRect, image.Rect(0, 0, 100, 100)},
		{"inner rect", image.Rect(0, 0, 984, 1231), image.Rect(0, 0, 984, 1231), ports.Rect{X: 4, Y: 8, W: 100, H: 200}, image.Rect(4, 8, 104, 208)},
		{"outside", image.Rect(0, 0, 984, 1231), image.Rect(0, 0, 3, 3), ports.Rect{X: 4, Y: 8, W: 100, H: 200}, image.Rectangle{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c.OpaqueRect = tc.rect
			got := opaqueCover(&c, tc.full, tc.dst)
			if got != tc.want {
				t.Fatalf("opaqueCover = %v, want %v", got, tc.want)
			}
		})
	}
}

// Building the draws of a window with a partly opaque child allocates
// nothing.
func TestSceneOpaqueRegionAllocations(t *testing.T) {
	r, err := New(64, 48)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	scene := ports.Scene{Seq: 1, Scale: 1.5, Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 20, H: 12}}}}
	contents := map[ports.WindowID]ports.SurfaceContent{1: opaqueRegionContent(t, 20, 12)}
	bounds := image.Rect(0, 0, 64, 48)
	frame := func() { _ = r.draws(scene, contents, r.frameDamage(r.target(), scene, bounds)) }
	frame()
	if allocs := testing.AllocsPerRun(50, frame); allocs != 0 {
		t.Errorf("partly opaque child draws: %.1f allocs/frame, want 0", allocs)
	}
}

// BenchmarkComposeOpaqueRegionChild composes a Firefox-like window moved every
// frame as a workspace swipe does, at 2560x1600 and scale 1.3: a 2x shm root
// of 984x1231 logical pixels under a dmabuf page of 1279x1600 pixels shown
// at 984x1231 (the viewport destination). "none" is a page with no opaque
// region (the root draws in full), "short" one with a region a logical pixel
// short of its right and bottom edges, "whole" one that covers it. The GPU is
// waited for after every frame. Select it with NEFERWL_VK_DEVICE.
func BenchmarkComposeOpaqueRegionChild(b *testing.B) {
	const w, h, lw, lh = 2560, 1600, 984, 1231
	for _, tc := range []struct {
		name   string
		opaque bool
		rect   ports.Rect
	}{
		{"none", false, ports.Rect{}},
		{"short", false, ports.Rect{W: lw - 1, H: lh - 1}},
		{"whole", true, ports.Rect{}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			r, err := New(w, h)
			if err != nil {
				b.Skipf("Vulkan unavailable: %v", err)
			}
			defer r.Close()
			linear := ports.DMABufFormat{Format: fourcc('A', 'R', '2', '4'), Modifier: 0}
			if !slices.Contains(r.DMABuf().Formats, linear) {
				b.Skipf("linear ARGB8888 not importable")
			}
			const pw, ph = 1279, 1600
			f := udmabuf(b, 1280, ph, func(x, y int) [4]byte { return [4]byte{byte(x), byte(y), 200, 255} })
			page := ports.SurfaceContent{Width: pw, Height: ph, LogicalW: lw, LogicalH: lh, Surface: 2, Version: 1,
				Opaque: tc.opaque, OpaqueRect: tc.rect,
				DMABuf: &ports.DMABuf{ID: 9, Width: pw, Height: ph, Format: linear.Format, Planes: []ports.DMABufPlane{{File: f, Stride: 5120}}}}
			root := solidContent(b, 2*lw, 2*lh, color.RGBA{R: 240, G: 240, B: 240, A: 255})
			root.ID, root.Surface, root.Seq, root.Version = 1, 1, 1, 1
			root.LogicalW, root.LogicalH = lw, lh
			root.Opaque = true
			root.Children = []ports.Subsurface{{SurfaceContent: page}}
			contents := map[ports.WindowID]ports.SurfaceContent{1: root}
			frame := func(i int) {
				x := i % 64
				scene := ports.Scene{Seq: uint64(i + 1), Scale: 1.3, Background: "#101010", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{X: x, W: lw, H: lh}}}}
				if err := render(r, scene, contents); err != nil {
					b.Fatal(err)
				}
				if err := r.waitFrame(r.submitted); err != nil {
					b.Fatal(err)
				}
			}
			for i := range 8 {
				frame(i)
			}
			b.ResetTimer()
			start := time.Now()
			for i := range b.N {
				frame(i + 8)
			}
			b.ReportMetric(float64(time.Since(start).Microseconds())/1000/float64(b.N), "ms/frame")
		})
	}
}

// crossingCovers is a root of w×h pixels under n opaque 10×10 children
// staggered over it, so that each cover crosses the root and the others.
func crossingCovers(t testing.TB, w, h, n int) ports.SurfaceContent {
	root := solidContent(t, w, h, color.RGBA{R: 255, A: 255})
	root.ID, root.Surface, root.Seq, root.Version = 1, 1, 1, 1
	for i := range n {
		child := solidContent(t, 10, 10, color.RGBA{G: 255, A: 255})
		child.Surface, child.Version, child.Opaque = uint64(2+i), 1, true
		root.Children = append(root.Children, ports.Subsurface{SurfaceContent: child, X: (i * 7) % (w - 10), Y: (i * 5) % (h - 10)})
	}
	return root
}

// A client chooses how many opaque subsurfaces cross its root, and each one
// cuts the root quad into strips: the split stops at maxSplitCovers covers
// or maxSplitStrips draws, and the root then draws whole (overdraw only, the
// covers above it still hide the rest).
func TestSceneSplitIsBounded(t *testing.T) {
	r, err := New(64, 48)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	const w, h = 60, 40
	scene := ports.Scene{Seq: 1, Scale: 1, Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: w, H: h}}}}
	full := image.Rect(0, 0, w, h)
	for _, n := range []int{1, maxSplitCovers, maxSplitCovers + 1, 40} {
		contents := map[ports.WindowID]ports.SurfaceContent{1: crossingCovers(t, w, h, n)}
		ds := r.draws(scene, contents, newDamage(r.target(), scene, image.Rect(0, 0, 64, 48)))
		rects := drawRects(ds)
		root := len(rects) - n // the children draw once each, after the root
		if root < 1 || root > maxSplitStrips {
			t.Errorf("%d covers: %d root draws, want 1..%d", n, root, maxSplitStrips)
		}
		if n > maxSplitCovers && (root != 1 || rects[0] != full) {
			t.Errorf("%d covers: root draws %v, want the whole quad %v", n, rects[:root], full)
		}
		if n == 1 && root < 2 {
			t.Errorf("one cover: %d root draws, want the quad split around it", root)
		}
		bounds := image.Rect(0, 0, 64, 48)
		frame := func() { _ = r.draws(scene, contents, r.frameDamage(r.target(), scene, bounds)) }
		frame()
		if allocs := testing.AllocsPerRun(20, frame); allocs != 0 {
			t.Errorf("%d covers: %.1f allocs/frame, want 0", n, allocs)
		}
	}
}

// With the split given up on, the covers above the whole root quad still
// show: the window's pixels are the children's inside them and the root's
// elsewhere.
func TestRendererSplitBoundKeepsRoot(t *testing.T) {
	r, err := New(64, 48)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	const w, h, n = 60, 40, 40
	scene := ports.Scene{Scale: 1, Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: w, H: h}}}}
	root := crossingCovers(t, w, h, n)
	if err := render(r, scene, map[ports.WindowID]ports.SurfaceContent{1: root}); err != nil {
		t.Fatal(err)
	}
	got := readPixels(t, r)
	red, green := color.RGBA{R: 255, A: 255}, color.RGBA{G: 255, A: 255}
	for y := range h {
		for x := range w {
			want := red
			for _, c := range root.Children {
				if image.Pt(x, y).In(image.Rect(c.X, c.Y, c.X+10, c.Y+10)) {
					want = green
				}
			}
			if g := got.At(x, y); g != want {
				t.Fatalf("At(%d,%d)=%v want %v", x, y, g, want)
			}
		}
	}
}
