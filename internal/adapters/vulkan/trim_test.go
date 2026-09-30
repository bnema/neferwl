package vulkan

import (
	"image/color"
	"slices"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

// trimSettled calls Trim at now until the frames the GPU was running
// finished and their deferred frees ran: Trim never waits for them.
func trimSettled(t *testing.T, r *Renderer, now time.Time) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := r.Trim(now); err != nil {
			t.Fatal(err)
		}
		if len(r.deferred) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d frees never ran", len(r.deferred))
		}
		time.Sleep(time.Millisecond)
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
	deadline := time.Now().Add(5 * time.Second)
	for !freed {
		if time.Now().After(deadline) {
			t.Fatal("finished frame never polled")
		}
		if err := r.Trim(time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if r.completed != r.frame {
		t.Fatalf("completed %d, frame %d", r.completed, r.frame)
	}
}
