package vulkan

import (
	"image/color"
	"slices"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	vk "github.com/bnema/purego-vulkan/vulkan"
)

// trimSettled waits for the submitted GPU fences, then lets Trim collect them.
func trimSettled(t *testing.T, r *Renderer, now time.Time) {
	t.Helper()
	for i := range r.slots {
		s := &r.slots[i]
		if s.busy {
			if result := r.dd.WaitForFences(r.device, 1, &s.fence, vk.Bool32(1), uint64(5*time.Second)); result != vk.Success {
				t.Fatalf("slot %d: GPU fence did not settle: %v", i, result)
			}
		}
	}
	if err := r.Trim(now); err != nil {
		t.Fatal(err)
	}
	if len(r.deferred) != 0 {
		t.Fatalf("%d frees never ran after all slot fences settled", len(r.deferred))
	}
}

// An idle output frees the pool mapping and GPU copies of a window it no
// longer draws once idleTTL passed, without rendering; the window drawn
// by the last frame stays, and a window coming back is copied again.
func TestTrimFreesUndrawnSHMWithoutRender(t *testing.T) {
	r, err := New(32, 32)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	a := solidContent(t, 16, 16, color.RGBA{255, 0, 0, 255})
	a.ID, a.Seq, a.Version = 1, 1, 1
	b := solidContent(t, 16, 16, color.RGBA{0, 0, 255, 255})
	b.ID, b.Seq, b.Version = 2, 1, 1
	contents := map[ports.WindowID]ports.SurfaceContent{1: a, 2: b}
	both := ports.Scene{Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 16, H: 16}}, {ID: 2, Rect: ports.Rect{X: 16, W: 16, H: 16}}}}
	onlyB := ports.Scene{Background: "#000000", Windows: []ports.SceneWindow{{ID: 2, Rect: ports.Rect{X: 16, W: 16, H: 16}}}}
	if err := render(r, both, contents); err != nil {
		t.Fatal(err)
	}
	if err := render(r, onlyB, contents); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1000, 0)
	for _, dt := range []time.Duration{0, 10 * time.Second, idleTTL - time.Second} {
		if err := r.Trim(start.Add(dt)); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.pools) != 2 || len(r.shm) != 2 {
		t.Fatalf("freed before idleTTL: pools %d, copies %d", len(r.pools), len(r.shm))
	}
	trimSettled(t, r, start.Add(idleTTL))
	if _, ok := r.pools[a.SHM.Pool]; ok || len(r.pools) != 1 {
		t.Fatalf("undrawn pool kept: %d pools", len(r.pools))
	}
	if _, ok := r.shm[shmKey{win: 1}]; ok || len(r.shm) != 1 {
		t.Fatalf("undrawn copies kept: %d surfaces", len(r.shm))
	}
	// Still idle: the window on screen stays.
	if err := r.Trim(start.Add(10 * idleTTL)); err != nil {
		t.Fatal(err)
	}
	if len(r.pools) != 1 || len(r.shm) != 1 {
		t.Fatalf("shown window freed: pools %d, copies %d", len(r.pools), len(r.shm))
	}
	copied := r.copied
	if err := render(r, both, contents); err != nil {
		t.Fatal(err)
	}
	if r.copied != copied+16*16*4 {
		t.Fatalf("returning window copied %d bytes", r.copied-copied)
	}
	if got := readPixels(t, r).RGBAAt(4, 4); got != (color.RGBA{255, 0, 0, 255}) {
		t.Fatalf("returning window shows %v", got)
	}
}

// A window drawn again within idleTTL keeps its caches: frames date them.
func TestTrimKeepsRecentlyDrawn(t *testing.T) {
	r, err := New(32, 32)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	a := solidContent(t, 16, 16, color.RGBA{255, 0, 0, 255})
	a.ID, a.Seq, a.Version = 1, 1, 1
	contents := map[ports.WindowID]ports.SurfaceContent{1: a}
	shown := ports.Scene{Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 16, H: 16}}}}
	empty := ports.Scene{Background: "#000000"}
	start := time.Unix(1000, 0)
	if err := render(r, shown, contents); err != nil {
		t.Fatal(err)
	}
	if err := render(r, empty, contents); err != nil {
		t.Fatal(err)
	}
	if err := r.Trim(start); err != nil {
		t.Fatal(err)
	}
	// Back on screen, then away again just before idleTTL.
	if err := render(r, shown, contents); err != nil {
		t.Fatal(err)
	}
	if err := render(r, empty, contents); err != nil {
		t.Fatal(err)
	}
	if err := r.Trim(start.Add(idleTTL - time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := r.Trim(start.Add(idleTTL)); err != nil {
		t.Fatal(err)
	}
	if len(r.pools) != 1 || len(r.shm) != 1 {
		t.Fatalf("drawn %v ago but freed", time.Second)
	}
	if err := r.Trim(start.Add(2*idleTTL - time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(r.pools) != 0 || len(r.shm) != 0 {
		t.Fatalf("kept after idleTTL: pools %d, copies %d", len(r.pools), len(r.shm))
	}
}

// A dmabuf import left undrawn is released by Trim without a frame.
func TestTrimReleasesUndrawnDMABuf(t *testing.T) {
	r, err := New(80, 32)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	linear := ports.DMABufFormat{Format: fourcc('A', 'R', '2', '4'), Modifier: 0}
	if !slices.Contains(r.DMABuf().Formats, linear) {
		t.Skipf("linear ARGB8888 not importable: %+v", r.DMABuf())
	}
	f := udmabuf(t, 64, 16, func(int, int) [4]byte { return [4]byte{0, 0, 255, 255} })
	buf := &ports.DMABuf{ID: 9, Width: 64, Height: 16, Format: linear.Format, Planes: []ports.DMABufPlane{{File: f, Stride: 256}}}
	scene := ports.Scene{Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: ports.Rect{W: 64, H: 16}}}}
	contents := map[ports.WindowID]ports.SurfaceContent{1: {ID: 1, Width: 64, Height: 16, DMABuf: buf}}
	if err := render(r, scene, contents); err != nil {
		t.Fatal(err)
	}
	if err := render(r, ports.Scene{Background: "#000000"}, nil); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1000, 0)
	if err := r.Trim(start); err != nil {
		t.Fatal(err)
	}
	trimSettled(t, r, start.Add(idleTTL))
	if len(r.imports) != 0 {
		t.Fatalf("imports %d", len(r.imports))
	}
}

// poll frees what a finished frame held without waiting for it.
//
//testpolicy:realtime real GPU frame completion polled through Trim, bounded by one timeout
func TestTrimPollsFinishedFrames(t *testing.T) {
	r, err := New(32, 32)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	if !r.syncFD {
		t.Skip("frames are finished on return without sync files")
	}
	if err := render(r, ports.Scene{Background: "#000000"}, nil); err != nil {
		t.Fatal(err)
	}
	freed := false
	r.retire(r.frame, func() { freed = true })
	timeout, tick := time.After(5*time.Second), time.NewTicker(time.Millisecond)
	defer tick.Stop()
	now := time.Now()
	for !freed {
		if err := r.Trim(now); err != nil {
			t.Fatal(err)
		}
		if freed {
			break
		}
		select {
		case <-timeout:
			t.Fatal("finished frame never polled")
		case <-tick.C:
		}
	}
	if r.completed != r.frame {
		t.Fatalf("completed %d, frame %d", r.completed, r.frame)
	}
}

// A window the last scene lists as Hidden (another workspace) loses its pool
// mapping and GPU copy after hiddenTTL, while a window that merely left the
// scene keeps them until idleTTL; the hidden window is copied again, with the
// right pixels, once a scene draws it.
func TestTrimFreesHiddenWindowsSooner(t *testing.T) {
	r, err := New(48, 32)
	if err != nil {
		t.Skipf("Vulkan unavailable: %v", err)
	}
	defer r.Close()
	contents := map[ports.WindowID]ports.SurfaceContent{}
	for id, c := range map[ports.WindowID]color.RGBA{1: {255, 0, 0, 255}, 2: {0, 255, 0, 255}, 3: {0, 0, 255, 255}} {
		sc := solidContent(t, 16, 16, c)
		sc.ID, sc.Seq, sc.Version = id, 1, 1
		contents[id] = sc
	}
	rect := func(i int) ports.Rect { return ports.Rect{X: 16 * i, W: 16, H: 16} }
	all := ports.Scene{Background: "#000000", Windows: []ports.SceneWindow{{ID: 1, Rect: rect(0)}, {ID: 2, Rect: rect(1)}, {ID: 3, Rect: rect(2)}}}
	// 1 left the scene, 2 is hidden, 3 stays on screen.
	away := ports.Scene{Background: "#000000", Windows: []ports.SceneWindow{{ID: 2, Rect: rect(1), Hidden: true}, {ID: 3, Rect: rect(2)}}}
	if err := render(r, all, contents); err != nil {
		t.Fatal(err)
	}
	if err := render(r, away, contents); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1000, 0)
	for _, dt := range []time.Duration{0, hiddenTTL - time.Second} {
		if err := r.Trim(start.Add(dt)); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.shm) != 3 || len(r.pools) != 3 {
		t.Fatalf("freed before hiddenTTL: pools %d, copies %d", len(r.pools), len(r.shm))
	}
	trimSettled(t, r, start.Add(hiddenTTL))
	if _, ok := r.shm[shmKey{win: 2}]; ok {
		t.Fatal("hidden window's copy kept past hiddenTTL")
	}
	if len(r.shm) != 2 || len(r.pools) != 2 {
		t.Fatalf("only the hidden window goes: pools %d, copies %d", len(r.pools), len(r.shm))
	}
	trimSettled(t, r, start.Add(idleTTL))
	if _, ok := r.shm[shmKey{win: 1}]; ok || len(r.shm) != 1 {
		t.Fatalf("window out of the scene kept past idleTTL: %d copies", len(r.shm))
	}
	// Shown again: copied once more, and correct.
	if err := render(r, all, contents); err != nil {
		t.Fatal(err)
	}
	if got := readPixels(t, r).RGBAAt(20, 4); got != (color.RGBA{0, 255, 0, 255}) {
		t.Fatalf("returning window shows %v", got)
	}
}
