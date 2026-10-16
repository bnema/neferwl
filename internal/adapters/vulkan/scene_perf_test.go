package vulkan

import (
	"image"
	"image/color"
	"slices"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
)

// TestRenderSteadyStateAllocations measures the cost of a small stable scene,
// on a normal and on a rotated output (orient works in place), and that
// building the draws of a scene with a moving window (a new Seq and window
// slice every frame, as an animation publishes) allocates nothing.
// A new scene Seq with one of two windows moved is a partial redraw seeded by
// sceneDelta and allocates nothing either.
func TestRenderSteadyStateAllocations(t *testing.T) {
	for _, tc := range []struct {
		name           string
		transform      ports.BufferTransform
		tw, th         int
		sceneW, sceneH int
	}{
		{"transform 0", 0, 32, 32, 32, 32},
		{"transform 1", 1, 32, 24, 24, 32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := New(tc.tw, tc.th)
			if err != nil {
				t.Skipf("Vulkan unavailable: %v", err)
			}
			defer r.Close()
			scene := ports.Scene{Seq: 1, Transform: tc.transform, Scale: 1, Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: tc.sceneW, H: tc.sceneH}}}}
			content := solidContent(t, 8, 8, color.RGBA{R: 255, A: 255})
			content.ID, content.Surface, content.Seq, content.Version = 1, 1, 1, 1
			contents := map[ports.WindowID]ports.SurfaceContent{1: content}
			frame := func() {
				if err := render(r, scene, contents); err != nil {
					t.Fatal(err)
				}
			}
			frame()
			if allocs := testing.AllocsPerRun(20, frame); allocs > 3 {
				t.Errorf("small steady-state Render: %.1f allocs/frame, want <=3 (syncFD path: %v)", allocs, r.syncFD)
			} else {
				t.Logf("small steady-state Render: %.1f allocs/frame", allocs)
			}
			// A moving window: scenes are prebuilt (core allocates them, not
			// the renderer), each with a fresh Seq, so every frame draws
			// everything again. The draw building must not allocate.
			var moving [4]ports.Scene
			for i := range moving {
				moving[i] = ports.Scene{Seq: uint64(100 + i), Transform: tc.transform, Scale: 1, Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{X: i, W: tc.sceneW - 4, H: tc.sceneH - 4}}}}
			}
			bounds := image.Rect(0, 0, r.width, r.height)
			n := 0
			frameDraws := func() {
				n++
				s := moving[n%len(moving)]
				_ = r.draws(s, contents, r.frameDamage(r.last, s, bounds))
			}
			frameDraws()
			if allocs := testing.AllocsPerRun(50, frameDraws); allocs != 0 {
				t.Errorf("moving window draws: %.1f allocs/frame, want 0", allocs)
			}
			// A content change under an unchanged scene Seq: a partial
			// redraw clipped to the changed buffer region, replicating the
			// partial sequence of Render. The stand-in target holds the
			// previous content Seq of the window, and is never updated, so
			// every run is damaged.
			dc := content
			dc.Seq, dc.Version = 2, 2
			dc.DamageHistory = []ports.SeqDamage{{Seq: 2, Rects: []ports.Rect{{X: 1, Y: 1, W: 2, H: 2}}}}
			pcontents := map[ports.WindowID]ports.SurfaceContent{1: dc}
			held := &target{valid: true, sceneSeq: scene.Seq, windows: map[ports.WindowID]heldWindow{1: {seq: 1, rect: image.Rect(0, 0, r.width, r.height)}}}
			var area image.Rectangle
			var whole bool
			partial := func() {
				dmg := r.frameDamage(held, scene, bounds)
				ds := r.draws(scene, pcontents, dmg)
				dmg.finish()
				ds = append(ds, draw{})
				copy(ds[1:], ds[:len(ds)-1])
				ds[0] = r.fillDraw(dmg.area, parseColor(scene.Background))
				ds = dmg.clip(ds)
				r.scratchDraws = ds
				r.orient(ds, scene.Transform)
				area, whole = dmg.area, dmg.all()
			}
			partial()
			if whole || area.Empty() || area.Dx()*area.Dy() >= bounds.Dx()*bounds.Dy() {
				t.Fatalf("not a partial redraw: all %v, area %v of %v", whole, area, bounds)
			}
			if allocs := testing.AllocsPerRun(50, partial); allocs != 0 {
				t.Errorf("partial redraw draws: %.1f allocs/frame, want 0", allocs)
			}
			// A new scene Seq with one window moved: the delta against the
			// scene the target holds seeds the partial redraw (A2). The
			// stand-in target holds the old scene and is never updated.
			half := tc.sceneW / 2
			content2 := solidContent(t, 8, 8, color.RGBA{G: 255, A: 255})
			content2.ID, content2.Surface, content2.Seq, content2.Version = 2, 2, 1, 1
			dcontents := map[ports.WindowID]ports.SurfaceContent{1: content, 2: content2}
			before := ports.Scene{Seq: 200, Transform: tc.transform, Scale: 1, Windows: []ports.SceneWindow{
				{ID: 1, Rect: ports.Rect{W: half, H: tc.sceneH}},
				{ID: 2, Rect: ports.Rect{X: half, W: tc.sceneW - half, H: tc.sceneH}},
			}}
			moved := before
			moved.Seq = 201
			moved.Windows = []ports.SceneWindow{before.Windows[0], {ID: 2, Rect: ports.Rect{X: half + 1, W: tc.sceneW - half - 1, H: tc.sceneH}}}
			full := image.Rect(0, 0, r.width, r.height)
			held = &target{valid: true, sceneSeq: before.Seq, scene: before, windows: map[ports.WindowID]heldWindow{
				1: {seq: 1, rect: image.Rect(0, 0, half, tc.sceneH)},
				2: {seq: 1, rect: image.Rect(half, 0, tc.sceneW, tc.sceneH)},
			}}
			seeded := func() {
				dmg := r.frameDamage(held, moved, bounds)
				ds := r.draws(moved, dcontents, dmg)
				dmg.finish()
				ds = append(ds, draw{})
				copy(ds[1:], ds[:len(ds)-1])
				ds[0] = r.fillDraw(dmg.area, parseColor(moved.Background))
				ds = dmg.clip(ds)
				r.scratchDraws = ds
				r.orient(ds, moved.Transform)
				area, whole = dmg.area, dmg.all()
			}
			seeded()
			if whole || area.Empty() || area.Dx()*area.Dy() >= full.Dx()*full.Dy() {
				t.Fatalf("sceneDelta did not seed a partial redraw: all %v, area %v of %v", whole, area, bounds)
			}
			if allocs := testing.AllocsPerRun(50, seeded); allocs != 0 {
				t.Errorf("sceneDelta-seeded partial redraw draws: %.1f allocs/frame, want 0", allocs)
			}
		})
	}
}

// A steady frame that draws a client dmabuf also records the ownership
// barriers (acquire and release) of its image, which allocate nothing. The
// frame still allocates more than the SHM scene: importing the client's
// read fence (measured 5.0 on radv; the semaphore retire is typed and costs
// no closure, locked by TestRetireSemaphoreAllocatesNothing). The budget
// keeps one alloc of headroom for drivers whose import path differs.
// Skipped without a GPU or /dev/udmabuf.
func TestRenderSteadyStateDMABufAllocations(t *testing.T) {
	r, err := New(80, 32)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	linear := ports.DMABufFormat{Format: fourcc('A', 'R', '2', '4'), Modifier: 0}
	if !slices.Contains(r.DMABuf().Formats, linear) {
		t.Skipf("linear ARGB8888 not importable")
	}
	f := udmabuf(t, 64, 16, func(int, int) [4]byte { return [4]byte{0, 0, 255, 255} })
	buf := &ports.DMABuf{ID: 7, Width: 64, Height: 16, Format: linear.Format, Planes: []ports.DMABufPlane{{File: f, Stride: 256}}}
	scene := ports.Scene{Seq: 1, Background: "#000000", Scale: 1, Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{X: 4, Y: 4, W: 64, H: 16}}}}
	contents := map[ports.WindowID]ports.SurfaceContent{1: {ID: 1, Surface: 1, Seq: 1, Version: 1, Width: 64, Height: 16, DMABuf: buf}}
	frame := func() {
		if err := render(r, scene, contents); err != nil {
			t.Fatal(err)
		}
	}
	frame()
	if len(r.imports) != 1 {
		t.Fatalf("imports %d: the frame does not take the dmabuf path", len(r.imports))
	}
	// Each new scene Seq draws the dmabuf again, with its barriers.
	var moving [4]ports.Scene
	for i := range moving {
		moving[i] = scene
		moving[i].Seq = uint64(10 + i)
		moving[i].Windows = []ports.SceneWindow{{ID: 1, Rect: ports.Rect{X: 4 + i, Y: 4, W: 64, H: 16}}}
	}
	n := 0
	moved := func() {
		n++
		if err := render(r, moving[n%len(moving)], contents); err != nil {
			t.Fatal(err)
		}
	}
	moved()
	allocs := testing.AllocsPerRun(20, moved)
	t.Logf("dmabuf Render: %.1f allocs/frame", allocs)
	if allocs > 6 {
		t.Errorf("dmabuf steady-state Render: %.1f allocs/frame, want <=6 (syncFD path: %v; a driver without sync-file export takes another path)", allocs, r.syncFD)
	}
}

// Retiring a frame's wait semaphore appends a typed entry to the reused
// deferred list: no closure, no allocation once the list has its capacity.
func TestRetireSemaphoreAllocatesNothing(t *testing.T) {
	r, err := New(32, 32)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	r.completed = 0
	retire := func() {
		// A null handle is a valid vkDestroySemaphore argument.
		r.retireSemaphore(r.completed+1, 0)
		r.completed++
		r.runDeferred()
	}
	retire()
	if allocs := testing.AllocsPerRun(50, retire); allocs != 0 {
		t.Errorf("retireSemaphore: %.1f allocs, want 0", allocs)
	}
	if len(r.deferred) != 0 {
		t.Errorf("%d deferred entries left after their frame completed", len(r.deferred))
	}
}

// TestSceneWalkUnchangedTiledSHM guards the GPU copy budget of an unchanged
// subsurface tree. Each child has its own real wl_shm pool.
func TestSceneWalkUnchangedTiledSHM(t *testing.T) {
	const tiles = 64
	r, err := New(128, 128)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	root := ports.SurfaceContent{ID: 1, Seq: 1, Surface: 1, Version: 1}
	for i := range tiles {
		child := solidContent(t, 8, 8, color.RGBA{R: uint8(i + 1), A: 255})
		child.ID, child.Seq, child.Surface, child.Version = 1, 1, uint64(i+2), 1
		root.Children = append(root.Children, ports.Subsurface{X: i % 8 * 8, Y: i / 8 * 8, SurfaceContent: child})
	}
	video := solidContent(t, 8, 8, color.RGBA{G: 255, A: 255})
	video.ID, video.Seq, video.Surface, video.Version = 1, 1, 66, 1
	root.Children = append(root.Children, ports.Subsurface{X: 80, Y: 0, SurfaceContent: video})
	scene := ports.Scene{Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 128, H: 128}}}}
	contents := map[ports.WindowID]ports.SurfaceContent{1: root}
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{16, 64} {
		contents[1] = ports.SurfaceContent{ID: 1, Seq: root.Seq, Surface: root.Surface, Version: root.Version, Children: root.Children[:count]}
		walk := func() {
			dmg := newDamage(&target{}, scene, image.Rect(0, 0, r.width, r.height))
			_ = r.draws(scene, contents, dmg)
		}
		walk()
		if allocs := testing.AllocsPerRun(20, walk); allocs > 8 {
			t.Errorf("%d tiles: %.1f allocations/walk, want <=8", count, allocs)
		} else {
			t.Logf("%d tiles: %.1f allocations/walk", count, allocs)
		}
	}
	contents[1] = root
	// The same renderer and scene are reused; measure only steady-state frame work.
	frame := func() {
		if err := render(r, scene, contents); err != nil {
			t.Fatal(err)
		}
	}
	frame()
	if allocs := testing.AllocsPerRun(20, frame); allocs > 16 {
		t.Errorf("steady-state Render: %.1f allocs/frame, want <=16", allocs)
	} else {
		t.Logf("steady-state Render: %.1f allocs/frame", allocs)
	}
	before := r.copied
	for i := range 100 {
		root.Seq++
		root.Children[tiles].Version++
		contents[1] = root
		if err := render(r, scene, contents); err != nil {
			t.Fatal(err)
		}
		if delta := r.copied - before; delta != 8*8*4 {
			t.Fatalf("publication %d copied %d bytes, want video only (%d)", i, delta, 8*8*4)
		}
		before = r.copied
	}
	// Reordering equal-sized children must not reuse another child's pixels.
	root.Children[0], root.Children[1] = root.Children[1], root.Children[0]
	root.Children[0].X, root.Children[1].X = 0, 8
	root.Seq++
	contents[1] = root
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	if got := readPixels(t, r).RGBAAt(4, 4); got != (color.RGBA{R: 2, A: 255}) {
		t.Fatalf("restacked tile has pixels %v, want second child's colour", got)
	}
}
