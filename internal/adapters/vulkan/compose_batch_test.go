package vulkan

import (
	"image"
	"image/color"
	"slices"
	"testing"
	"unsafe"

	"github.com/bnema/neferwl/internal/ports"
	vk "github.com/bnema/purego-vulkan/vulkan"
)

func TestInstanceDrawLayout(t *testing.T) {
	if unsafe.Sizeof(drawParameters{}) != 112 || unsafe.Sizeof(instanceDraw{}) != 160 || unsafe.Offsetof(instanceDraw{}.resource) != 112 || unsafe.Offsetof(instanceDraw{}.round) != 128 || unsafe.Offsetof(instanceDraw{}.roundInner) != 144 {
		t.Fatal("draw layout differs from compose.vert std430 layout")
	}
}

func TestComposeBatchPaintOrder(t *testing.T) {
	a, b := &shmCopy{}, &shmCopy{}
	ds := []draw{{}, {shm: a}, {}, {shm: b}, {shm: a}}
	for i := range ds {
		ds[i].pc.rect[0] = int32(i)
	}
	var batch composeBatch
	for _, dr := range ds {
		if !batch.add(dr, 2, 20) {
			t.Fatal("unexpected split")
		}
	}
	if batch.used != 2 {
		t.Fatalf("resources = %d, want 2", batch.used)
	}
	for i, want := range []uint32{0, 0, 0, 1, 0} {
		if batch.instances[i].resource[0] != want || batch.instances[i].pc.rect[0] != int32(i) {
			t.Fatalf("draw %d reordered or assigned wrong resource: %+v", i, batch.instances[i])
		}
	}
	if batch.add(draw{shm: &shmCopy{}}, 2, 20) {
		t.Fatal("accepted resource beyond descriptor capacity")
	}
	if batch.add(draw{}, 2, len(ds)) {
		t.Fatal("accepted draw beyond buffer capacity")
	}
	batch = composeBatch{}
	for _, dr := range ds[:3] {
		if !batch.add(dr, 1, 20) {
			t.Fatal("solid split a single-resource run")
		}
	}
	if batch.add(ds[3], 1, 20) {
		t.Fatal("single-resource batch accepted another resource")
	}
}

func TestComposeResourceLimit(t *testing.T) {
	limits := vk.PhysicalDeviceLimits{
		MaxPerStageDescriptorSamplers: 128, MaxDescriptorSetSamplers: 128,
		MaxPerStageDescriptorSampledImages: 128, MaxDescriptorSetSampledImages: 128,
		MaxPerStageDescriptorStorageBuffers: 64, MaxDescriptorSetStorageBuffers: 65,
		MaxPerStageResources: 193,
	}
	if got := composeResourceLimit(limits); got != 64 {
		t.Fatalf("limit = %d, want 64", got)
	}
	limits.MaxPerStageResources = 192
	if got := composeResourceLimit(limits); got != 63 {
		t.Fatalf("color attachment not counted: %d, want 63", got)
	}
	limits.MaxDescriptorSetStorageBuffers = 5
	if got := composeResourceLimit(limits); got != 4 {
		t.Fatalf("instance buffer not counted: %d, want 4", got)
	}
	limits.MaxPerStageDescriptorSamplers = 4
	if got := composeResourceLimit(limits); got != 2 {
		t.Fatalf("two image planes not counted: %d, want 2", got)
	}
	limits.MaxDescriptorSetStorageBuffers = 0
	if got := composeResourceLimit(limits); got != 0 {
		t.Fatalf("zero limit underflowed: %d", got)
	}
}

// Both shader variants preserve ordered blending across batch boundaries,
// resource changes, and more batches than the slot's initial slice capacity.
func TestComposeInstancedBlendAndBatches(t *testing.T) {
	for _, tc := range []struct {
		name      string
		single    bool
		resources uint32
	}{
		{"indexed", false, 3},
		{"single resource", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := New(16, 16)
			if err != nil {
				t.Skipf("Vulkan unavailable: %v", err)
			}
			defer r.Close()
			if !tc.single && !r.compose.indexing {
				t.Skip("nonuniform descriptor indexing unavailable")
			}
			// Rebuild only the composition pipeline/layout before rendering,
			// to exercise bounded tables and the feature-free shader variant.
			r.destroyComposer()
			r.compose.resources, r.compose.indexing = tc.resources, !tc.single
			if err := r.createComposer(); err != nil {
				t.Fatal(err)
			}
			scene := ports.Scene{Background: "#000000"}
			contents := make(map[ports.WindowID]ports.SurfaceContent)
			var reference [3]float64
			for i := range 7 {
				id := ports.WindowID(i + 1)
				pixel := [4]byte{byte(12 + i*15), byte(90 - i*9), byte(20 + i*12), 128}
				for ch := range 3 {
					reference[ch] = float64(pixel[2-ch]) + reference[ch]*(127.0/255)
				}
				c := *shmContent(t, 16, 16, 64, fill(16, 16, pixel))
				c.ID, c.Seq, c.Version = id, 1, 1
				contents[id] = c
				scene.Layers = append(scene.Layers, ports.SceneLayer{ID: id, Layer: ports.LayerOverlay, Rect: ports.Rect{W: 16, H: 16}})
			}
			for range 4 {
				if err := render(r, scene, contents); err != nil {
					t.Fatal(err)
				}
				if got := readPixels(t, r).RGBAAt(8, 8); !near(got, color.RGBA{R: uint8(reference[0] + 0.5), G: uint8(reference[1] + 0.5), B: uint8(reference[2] + 0.5), A: 255}, 2) {
					t.Fatalf("ordered translucent overlays = %v", got)
				}
			}
			wantBatches := (7 + int(tc.resources) - 1) / int(tc.resources)
			for i := range r.slots {
				if len(r.slots[i].batches) != wantBatches {
					t.Fatalf("slot %d batches = %d, want %d", i, len(r.slots[i].batches), wantBatches)
				}
			}
			// Replace a resource: both slots must refresh their descriptors.
			oldKeys := [frameSlots][maxComposeResources]resourceKey{}
			for i := range r.slots {
				oldKeys[i] = r.slots[i].batches[wantBatches-1].cached
			}
			replacement := solidContent(t, 8, 8, color.RGBA{B: 255, A: 255})
			replacement.LogicalW, replacement.LogicalH = 16, 16
			replacement.ID, replacement.Seq, replacement.Version = 99, 2, 2
			contents[7] = replacement
			for range 4 {
				if err := render(r, scene, contents); err != nil {
					t.Fatal(err)
				}
				if got := readPixels(t, r).RGBAAt(8, 8); got != (color.RGBA{B: 255, A: 255}) {
					t.Fatalf("replaced resource = %v", got)
				}
			}
			for i := range r.slots {
				if r.slots[i].batches[wantBatches-1].cached == oldKeys[i] {
					t.Fatalf("slot %d retained old resource descriptors", i)
				}
			}
		})
	}
}

func TestComposePreparedReuseAndGrowth(t *testing.T) {
	r, err := New(16, 16)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	slot := &r.slots[0]
	ds := []draw{r.fillDraw(image.Rect(0, 0, 16, 16), [3]uint8{255})}
	if _, err := r.prepareCompose(slot, ds); err != nil {
		t.Fatal(err)
	}
	batch := slot.batches[0]
	initialSize := batch.gpu.size
	large := slices.Repeat(ds, 100)
	if _, err := r.prepareCompose(slot, large); err != nil {
		t.Fatal(err)
	}
	if batch.gpu.size <= initialSize || batch.drawBuffer.Buffer != batch.gpu.buffer || batch.count != 100 {
		t.Fatal("grown draw buffer not rebound")
	}
	if allocs := testing.AllocsPerRun(50, func() {
		if _, err := r.prepareCompose(slot, large); err != nil {
			t.Fatal(err)
		}
	}); allocs != 0 {
		t.Fatalf("steady compose upload: %.1f allocations, want zero", allocs)
	}
	if batches, err := r.prepareCompose(slot, nil); err != nil || len(batches) != 0 {
		t.Fatal("empty pass returned batches")
	}
	// Lower the draw range to force a split independently of descriptors.
	r.maxRange = 2 * int(unsafe.Sizeof(instanceDraw{}))
	if batches, err := r.prepareCompose(slot, ds); err != nil || len(batches) != 1 {
		t.Fatal("single draw did not fit")
	}
	if batches, err := r.prepareCompose(slot, slices.Repeat(ds, 5)); err != nil || len(batches) != 3 || batches[0].count != 2 || batches[2].count != 1 {
		t.Fatal("draw-buffer range did not split into 2,2,1")
	}
	// Shrink the scene and age its unused batches beyond the frame TTL.
	r.frame += importTTL + 1
	if batches, err := r.prepareCompose(slot, ds); err != nil || len(batches) != 1 || len(slot.batches) != 1 {
		t.Fatal("unused batches retained beyond frame TTL")
	}
}

// Zero handles: release, freeShmCopy and trimCompose make no Vulkan call.
func TestComposeReleaseInvalidatesCachedDescriptors(t *testing.T) {
	im, shm := &imported{fd: -1}, &shmCopy{}
	other := resourceKey{shm: &shmCopy{}}
	for _, tc := range []struct {
		name    string
		key     resourceKey
		release func(*Renderer)
	}{
		{"dmabuf", resourceKey{im: im}, func(r *Renderer) { r.release(im) }},
		{"shm", resourceKey{shm: shm}, func(r *Renderer) { r.freeShmCopy(shm) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Renderer{}
			for i := range r.slots {
				hit := &composeBatch{valid: true}
				hit.keys[0], hit.cached[0], hit.cached[1] = tc.key, tc.key, other
				miss := &composeBatch{valid: true}
				miss.cached[0] = other
				r.slots[i].batches = []*composeBatch{hit, miss}
			}
			tc.release(r)
			for i := range r.slots {
				hit, miss := r.slots[i].batches[0], r.slots[i].batches[1]
				if hit.valid || hit.cached[0] != (resourceKey{}) || hit.keys[0] != (resourceKey{}) || hit.cached[1] != other {
					t.Fatalf("slot %d: released resource kept its descriptor cache", i)
				}
				if !miss.valid || miss.cached[0] != other {
					t.Fatalf("slot %d: unrelated batch invalidated", i)
				}
			}
		})
	}
}

func TestTrimComposeKeepsInFlightAndRecentBatches(t *testing.T) {
	const frame = 100
	for _, tc := range []struct {
		name   string
		busy   bool
		cutoff uint64
		last   []uint64 // per batch, in slot order
		want   int
	}{
		{"idle stale", false, 50, []uint64{10, 20}, 0},
		{"busy stale", true, 50, []uint64{10, 20}, 2},
		{"newer than cutoff", false, 50, []uint64{10, 60}, 2},
		{"current frame", false, frame, []uint64{10, frame}, 2},
		{"only trailing stale", false, 50, []uint64{60, 10}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Renderer{frame: frame}
			s := &r.slots[0]
			s.busy = tc.busy
			for _, last := range tc.last {
				s.batches = append(s.batches, &composeBatch{last: last})
			}
			r.trimCompose(tc.cutoff)
			if len(s.batches) != tc.want {
				t.Fatalf("kept %d batches, want %d", len(s.batches), tc.want)
			}
		})
	}
}
