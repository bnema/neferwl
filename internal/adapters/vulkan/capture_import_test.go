package vulkan

import (
	"context"
	"github.com/bnema/neferwl/internal/adapters/syncfile"
	"github.com/bnema/neferwl/internal/domain"
	"image"
	"testing"
)

func TestCaptureDMABufCopyAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation measurements require non-race runtime")
	}
	r := newTestRenderer(t, 16, 8)
	defer r.Close()
	requireCaptureSync(t, r)
	buf := castLinear(t, r.gpu, domain.CastXRGB8888, 16, 8)
	if err := render(r, domain.Scene{Background: "#3060c0"}, nil); err != nil {
		t.Fatal(err)
	}
	cf := captureWait(t, r)
	defer r.EndCapture(cf)
	cycle := func() {
		castSubmit(t, cf, image.Rect(0, 0, 16, 8), buf)
		if err := r.reapCopies(cf.(*captureFrame).slot); err != nil {
			t.Fatal(err)
		}
	}
	cycle()
	// os.NewFile allocates its public wrapper and internal file (2 total).
	// The caller owns Close; GPU completion cannot authorize wrapper reuse.
	if allocs := testing.AllocsPerRun(100, cycle); allocs > 2 {
		t.Fatalf("%.1f allocs/copy, budget 2 caller-owned file allocations", allocs)
	}
	before := openFDs(t)
	result := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			cycle()
		}
	})
	t.Logf("%s %s", result.String(), result.MemString())
	if after := openFDs(t); after != before {
		t.Fatalf("fd count %d -> %d", before, after)
	}
}

func TestCaptureRetainedCastImage(t *testing.T) {
	r := newTestRenderer(t, 16, 8)
	defer r.Close()
	requireCaptureSync(t, r)
	buf := castLinear(t, r.gpu, domain.CastXRGB8888, 16, 8)
	allocationFDs := openFDs(t)
	e := r.gpu.castImages[buf.ID]
	if buf.ID == 0 || e == nil || e.img == nil {
		t.Fatal("allocation not retained")
	}
	original := e.img
	if err := render(r, domain.Scene{Background: "#3060c0"}, nil); err != nil {
		t.Fatal(err)
	}
	cf := captureWait(t, r)
	defer r.EndCapture(cf)
	slot := cf.(*captureFrame).slot
	for range 20 {
		castSubmit(t, cf, image.Rect(0, 0, 16, 8), buf)
		if len(slot.copies) != 1 || slot.copies[0].img != original || slot.copies[0].cached != e {
			t.Fatal("copy imported instead of using exporting image")
		}
		if err := r.reapCopies(slot); err != nil {
			t.Fatal(err)
		}
	}
	warmed := openFDs(t)
	// Keep a submitted copy reference across removal, even if this fast GPU
	// already completed it. Only fence reclamation may release that reference.
	done, err := cf.CopyTo(image.Rect(0, 0, 16, 8), buf)
	if err != nil {
		t.Fatal(err)
	}
	r.gpu.ReleaseCast(buf)
	if r.gpu.castImages[buf.ID] != nil || !e.released || e.img != original || e.refs != 1 {
		t.Fatal("release destroyed referenced image")
	}
	if done != nil {
		if err := syncfile.Wait(context.Background(), done); err != nil {
			t.Fatal(err)
		}
		_ = done.Close()
	}
	if err := r.reapCopies(slot); err != nil {
		t.Fatal(err)
	}
	if e.refs != 0 || e.img != nil || original.image != 0 || original.memory != 0 {
		t.Fatal("completed removal retained resources")
	}
	for _, p := range buf.Planes {
		_ = p.File.Close()
	}
	r.EndCapture(cf)
	r.Close()
	if after := openFDs(t); after >= allocationFDs || after >= warmed {
		t.Fatalf("fds allocated=%d warmed=%d released=%d", allocationFDs, warmed, after)
	}
}

func TestCaptureDMACopyObjectReuse(t *testing.T) {
	r := newTestRenderer(t, 16, 8)
	defer r.Close()
	requireCaptureSync(t, r)
	buf := castLinear(t, r.gpu, domain.CastXRGB8888, 16, 8)
	if err := render(r, domain.Scene{Background: "#3060c0"}, nil); err != nil {
		t.Fatal(err)
	}
	cf := captureWait(t, r)
	defer r.EndCapture(cf)
	slot := cf.(*captureFrame).slot
	castSubmit(t, cf, image.Rect(0, 0, 16, 8), buf)
	first := slot.copies[0]
	cmd, fence, done, wait := first.cmd, first.fence, first.done, first.wait
	if err := r.reapCopies(slot); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		castSubmit(t, cf, image.Rect(0, 0, 16, 8), buf)
		c := slot.copies[0]
		if c != first || c.cmd != cmd || c.fence != fence || c.done != done || c.wait != wait {
			t.Fatal("completed copy objects not reused")
		}
		if err := r.reapCopies(slot); err != nil {
			t.Fatal(err)
		}
	}
	// Export failure leaves a binary signal unconsumed: discard that object.
	first.exported = false
	r.dmaIdle = nil
	r.recycleDMACopy(first)
	if first.cmd != 0 || first.done != 0 || len(r.dmaIdle) != 0 {
		t.Fatal("unconsumed signal recycled")
	}
}
