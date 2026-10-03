package drm

import (
	"runtime"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// mallocsPerRun is testing.AllocsPerRun without the integer truncation.
func mallocsPerRun(runs int, f func()) float64 {
	f()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for range runs {
		f()
	}
	runtime.ReadMemStats(&after)
	return float64(after.Mallocs-before.Mallocs) / float64(runs)
}

func scanoutAllocsOutput(t *testing.T) (*Output, ports.SurfaceContent) {
	o, _, _ := testOutput(t)
	o.cursor.image = true
	o.cursor.Move(12, 34)
	o.clientFBs[9] = &clientFB{fbID: 80}
	return o, ports.SurfaceContent{ID: 1, Width: 200, Height: 100, DMABuf: &ports.DMABuf{ID: 9}}
}

// A steady-state direct-scanout commit builds its request, and the flat
// ioctl arrays, in buffers it reuses.
//
// The real kmsDevice on an invalid fd runs the whole request path up to the
// failing ioctl: exactly zero allocations. testify's own bookkeeping per
// mocked call allocates, so the success path (lifecycle, cursor, VRR) is
// measured on the generated mockkms against the same mocked call made
// directly: the commit may add nothing to it.
func TestCommitFrameAllocations(t *testing.T) {
	t.Run("request and ioctl arrays", func(t *testing.T) {
		o, c := scanoutAllocsOutput(t)
		o.k = kmsDevice{fd: -1}
		rect := fullPlaneRect(200, 100)
		commit := func() { _, _ = o.commitScanoutRect(80, c, pendingFrame{}, rect) }
		commit() // grow the buffers
		if len(o.frameReq.objs) != 3 || len(o.frameReq.counts) != 3 || len(o.frameReq.flatProps) == 0 {
			t.Fatalf("frame request %+v", o.frameReq)
		}
		if n := testing.AllocsPerRun(100, commit); n != 0 {
			t.Fatalf("direct scanout commit: %.1f allocs, want 0", n)
		}
	})
	t.Run("success path", func(t *testing.T) {
		o, c := scanoutAllocsOutput(t)
		k := newMockkms(t)
		k.EXPECT().commit(mock.Anything, mock.Anything, mock.Anything).Return(nil)
		o.k = k // no recorder: it copies every request
		rect := fullPlaneRect(200, 100)
		commit := func() {
			if _, err := o.commitScanoutRect(80, c, pendingFrame{}, rect); err != nil {
				t.Fatal(err)
			}
		}
		commit()
		// The mock boxes flags and userData (a fresh serial per commit):
		// the reference call passes them as variables too.
		serial, flags := uint64(1<<20), uint32(atomicNonblock|flipEventFlag)
		ref := mallocsPerRun(1000, func() {
			serial += 1 << userKindBits
			mockCallOuter(k, &o.frameReq, flags, serial|userFrame)
		})
		got := mallocsPerRun(1000, commit)
		t.Logf("commit %.2f allocs, the mocked call alone %.2f", got, ref)
		// A heap profile puts the whole difference inside testify (its
		// call-site lookup allocates per stack frame, and the stacks
		// differ by a frame or so); the first subtest proves the commit
		// itself allocates zero. A leaked request would add one per
		// slice and grow far past this margin (before: 9 more).
		if got-ref > 3 {
			t.Fatalf("direct scanout commit: %.2f allocs, %.2f of them the mock's", got, ref)
		}
	})
}

// mockCallOuter and mockCallInner stand where commitScanoutRect and
// commitWithRect stand: testify's call-site lookup allocates per stack
// frame, so the reference call must be as deep as the real one.
//
//go:noinline
func mockCallOuter(k *mockkms, req *atomicReq, flags uint32, user uint64) {
	mockCallInner(k, req, flags, user)
}

//go:noinline
func mockCallInner(k *mockkms, req *atomicReq, flags uint32, user uint64) {
	_ = k.commit(req, flags, user)
}

// A retry resets the frame request and builds it again: the second commit
// carries the retried frame, not the first one's leftovers, and the caller
// never reads the request after the recursive call.
func TestCommitFrameRetryRebuildsRequest(t *testing.T) {
	o, _, commits := testOutput(t, unix.EINVAL)
	o.shown, o.vrrOn = 1, true
	o.clientFBs[9] = &clientFB{fbID: 80}
	c := ports.SurfaceContent{DMABuf: &ports.DMABuf{ID: 9}, Async: true}
	// The async attempt is refused, then retried synchronously with the
	// full plane state in the same request object.
	ok, err := o.commitScanout(80, c, pendingFrame{})
	if !ok || err != nil || len(*commits) != 2 {
		t.Fatalf("ok=%v err=%v commits=%d", ok, err, len(*commits))
	}
	first, second := (*commits)[0], (*commits)[1]
	if len(first.req.objs) != 1 || first.req.objs[0] != tPrimary || len(first.req.props[0]) != 1 {
		t.Fatalf("async request %+v", first.req)
	}
	if v, ok := second.req.value(tPrimary, pFB); !ok || v != 80 {
		t.Fatalf("retry primary fb %d %v", v, ok)
	}
	if _, ok := second.req.value(tPrimary, 17); !ok {
		t.Fatal("retry lost the plane geometry")
	}
	if _, ok := second.req.value(tCrtc, pVRR); !ok {
		t.Fatal("retry lost VRR")
	}
	// The retry's request shows no stale entry twice.
	for i, ps := range second.req.props {
		seen := map[uint32]bool{}
		for _, p := range ps {
			if seen[p.prop] {
				t.Fatalf("object %d: property %d twice", second.req.objs[i], p.prop)
			}
			seen[p.prop] = true
		}
	}
}

// The cursor-refused retry rebuilds the request with the cursor off while
// the TEST_ONLY probe uses its own request.
func TestCommitFrameCursorRetryKeepsProbeApart(t *testing.T) {
	var frameSeen []*atomicReq
	o, k, commits := testOutput(t)
	_ = k
	o.cursor.image = true
	o.cursor.Move(1, 2)
	// First real commit refused with EINVAL, the cursor probe refused too,
	// then the retry without the cursor passes.
	k2 := newMockkms(t)
	n := 0
	k2.EXPECT().commit(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(func(r *atomicReq, flags uint32, _ uint64) error {
		n++
		frameSeen = append(frameSeen, r)
		if n <= 2 {
			return unix.EINVAL
		}
		return nil
	})
	o.k = k2
	if err := o.commitFrame(70, nil, false, false, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	if n != 3 || !o.cursor.off {
		t.Fatalf("commits %d cursorOff %v", n, o.cursor.off)
	}
	if frameSeen[0] != &o.frameReq || frameSeen[1] == &o.frameReq || frameSeen[2] != &o.frameReq {
		t.Fatal("probe must not reuse the frame request, retry must")
	}
	if _, ok := o.frameReq.value(tCursor, pFB); !ok {
		t.Fatal("cursor plane not turned off in the retry")
	}
	if v, _ := o.frameReq.value(tCursor, pFB); v != 0 {
		t.Fatalf("retry still shows the cursor: fb %d", v)
	}
	_ = commits
}

// kmsDevice.commit fills the request's own scratch: once grown it allocates
// nothing. The bad fd makes the ioctl fail before reaching a kernel.
func TestKMSDeviceCommitAllocations(t *testing.T) {
	k := kmsDevice{fd: -1}
	req := &atomicReq{}
	build := func() {
		req.reset()
		for i := range 3 {
			obj := uint32(10 + i)
			for p := range 8 {
				req.set(obj, uint32(p+1), uint64(p))
			}
		}
	}
	build()
	if err := k.commit(req, atomicTestOnly, 0); err == nil {
		t.Fatal("ioctl on fd -1 succeeded")
	}
	if len(req.counts) != 3 || len(req.flatProps) != 24 || len(req.flatVals) != 24 || req.counts[1] != 8 {
		t.Fatalf("scratch counts %v props %d vals %d", req.counts, len(req.flatProps), len(req.flatVals))
	}
	if n := testing.AllocsPerRun(100, func() {
		build()
		_ = k.commit(req, atomicTestOnly, 0)
	}); n != 0 {
		t.Fatalf("build and commit: %.1f allocs", n)
	}
}

func TestAtomicReqResetKeepsCapacityAndDropsContent(t *testing.T) {
	r := &atomicReq{}
	r.set(1, 1, 5)
	r.set(1, 2, 6)
	r.set(2, 1, 7)
	r.reset()
	if len(r.objs) != 0 || len(r.props) != 0 {
		t.Fatalf("not empty: %+v", r)
	}
	r.set(2, 9, 1)
	if len(r.objs) != 1 || len(r.props) != 1 || len(r.props[0]) != 1 || r.props[0][0] != (propValue{9, 1}) {
		t.Fatalf("stale content after reset: %+v", r)
	}
	if _, ok := r.value(1, 1); ok {
		t.Fatal("stale object")
	}
}
