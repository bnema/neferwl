package drm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
)

// pipeFence returns a pipe standing in for a sync file: its read end is
// readable (signalled) once the write end got a byte. Both ends close with
// the test.
func pipeFence(t *testing.T) (fence, signal *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	return r, w
}

func TestParseVersion(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want [3]int
		ok   bool
	}{
		{"560.35.03\n", [3]int{560, 35, 3}, true},
		{"615.78.08", [3]int{615, 78, 8}, true},
		{"550.120", [3]int{550, 120, 0}, true},
		{"570", [3]int{570, 0, 0}, true},
		{"", [3]int{}, false},
		{"abc", [3]int{}, false},
		{"560.x.3", [3]int{}, false},
		{"1.2.3.4", [3]int{}, false},
		{"-1.2", [3]int{}, false},
		{"+560.35.3", [3]int{}, false},
		{"560.+35.3", [3]int{}, false},
		{"560.-5.3", [3]int{}, false},
		{"560..3", [3]int{}, false},
		{"99999999999.1.1", [3]int{}, false},
	} {
		got, err := parseVersion(tc.in)
		if (err == nil) != tc.ok || tc.ok && got != tc.want {
			t.Errorf("parseVersion(%q) = %v, %v", tc.in, got, err)
		}
	}
}

// The gate opens for nvidia-drm before 560.35.3 or of unknown version, and
// for no other driver.
func TestBrokenInFence(t *testing.T) {
	for _, tc := range []struct {
		name, driver, version string
		err                   error
		want                  bool
	}{
		{"amd", "amdgpu", "", nil, false},
		{"intel", "i915", "garbage", nil, false},
		{"old nvidia", driverNVIDIA, "555.58.02", nil, true},
		{"one patch before", driverNVIDIA, "560.35.2", nil, true},
		{"minor before", driverNVIDIA, "560.34.99", nil, true},
		{"fixed", driverNVIDIA, "560.35.03\n", nil, false},
		{"patch after", driverNVIDIA, "560.35.5", nil, false},
		{"minor after", driverNVIDIA, "560.36.0", nil, false},
		{"major after", driverNVIDIA, "561.0.0", nil, false},
		{"newer", driverNVIDIA, "615.78.08", nil, false},
		{"unreadable", driverNVIDIA, "", errors.New("no such file"), true},
		{"unparsable", driverNVIDIA, "n/a", nil, true},
	} {
		if got := brokenInFence(tc.driver, tc.version, tc.err); got != tc.want {
			t.Errorf("%s: brokenInFence = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The driver of a DRM device is read with DRM_IOCTL_VERSION. A render node
// answers it too: skips without one.
func TestDriverName(t *testing.T) {
	nodes, _ := filepath.Glob("/dev/dri/renderD*")
	if len(nodes) == 0 {
		t.Skip("no /dev/dri/renderD* node")
	}
	for _, n := range nodes {
		f, err := os.OpenFile(n, os.O_RDWR|unix.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		defer f.Close()
		name, err := driverName(int(f.Fd()))
		if err != nil || name == "" {
			t.Fatalf("driverName(%s) = %q, %v", n, name, err)
		}
		t.Logf("%s: %s", n, name)
		return
	}
	t.Skip("no render node can be opened")
}

// With CPU waits no commit carries IN_FENCE_FD; otherwise the plane does.
func TestFenceProp(t *testing.T) {
	f, _ := pipeFence(t)
	for _, cpu := range []bool{false, true} {
		o, _, _ := testOutput(t)
		o.cpuFences = cpu
		req := &atomicReq{}
		o.fenceProp(req, o.primary, f)
		o.fenceProp(req, o.primary, nil)
		v, ok := req.value(o.primary.id, o.primary.prop("IN_FENCE_FD"))
		if ok == cpu || ok && v != uint64(f.Fd()) {
			t.Errorf("cpuFences=%v: IN_FENCE_FD %d, set %v", cpu, v, ok)
		}
	}
}

// waitFences returns for a fence that signalled, for one that never will
// once the output stops, and at once where fences go to the kernel.
//
//testpolicy:realtime real pipe fence polling is performed by waitFences
func TestWaitFences(t *testing.T) {
	returns := func(t *testing.T, o *Output, fences ...*os.File) {
		t.Helper()
		done := make(chan struct{})
		go func() { o.waitFences(fences...); close(done) }()
		select {
		case <-done:
		case <-time.After(30 * time.Second): // failure guard only
			t.Fatal("waitFences did not return")
		}
	}
	t.Run("signalled", func(t *testing.T) {
		o, _, _ := testOutput(t)
		o.cpuFences = true
		f, w := pipeFence(t)
		_, _ = w.Write([]byte{1})
		returns(t, o, nil, f)
	})
	t.Run("never signalled, output stopped", func(t *testing.T) {
		o, _, _ := testOutput(t)
		o.cpuFences = true
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		o.runContext = ctx
		f, _ := pipeFence(t)
		returns(t, o, f)
		if o.flipStats.fenceTimeouts != 0 {
			t.Fatal("a stopped output counted a fence timeout")
		}
	})
	t.Run("kernel waits", func(t *testing.T) {
		o, _, _ := testOutput(t)
		f, _ := pipeFence(t)
		returns(t, o, f)
	})
}

// A fence wait that hit its limit counts in the stats, and warns at most
// once per fenceWarnEvery.
func TestWaitFencesTimeoutIsCountedAndRateLimited(t *testing.T) {
	o, _, _ := testOutput(t)
	o.cpuFences = true
	o.fenceLimit = time.Millisecond
	f, _ := pipeFence(t)
	o.waitFences(f)
	if o.flipStats.fenceTimeouts != 1 || o.fenceWarnAt.IsZero() {
		t.Fatalf("timeouts %d, warned at %v", o.flipStats.fenceTimeouts, o.fenceWarnAt)
	}
	first := o.fenceWarnAt
	o.waitFences(f)
	if o.flipStats.fenceTimeouts != 2 || !o.fenceWarnAt.Equal(first) {
		t.Fatalf("timeouts %d; warning not rate-limited (%v then %v)", o.flipStats.fenceTimeouts, first, o.fenceWarnAt)
	}
}

// Without CPU fence waits every fence is ready; with them only a signalled
// client fence is, and one that cannot be polled is not.
func TestClientFenceReady(t *testing.T) {
	f, w := pipeFence(t)
	o, _, _ := testOutput(t)
	if !o.clientFenceReady(f) || !o.clientFenceReady(nil) {
		t.Fatal("fence not ready without CPU waits")
	}
	o.cpuFences = true
	if !o.clientFenceReady(nil) {
		t.Fatal("no fence must be ready")
	}
	if o.clientFenceReady(f) {
		t.Fatal("pending client fence ready")
	}
	_, _ = w.Write([]byte{1})
	if !o.clientFenceReady(f) {
		t.Fatal("signalled client fence not ready")
	}
	closed, cw := pipeFence(t)
	closed.Close()
	cw.Close()
	if o.clientFenceReady(closed) {
		t.Fatal("closed fence ready")
	}
}

// Polling a fence allocates nothing.
func TestClientFenceReadyAllocatesNothing(t *testing.T) {
	f, _ := pipeFence(t)
	o, _, _ := testOutput(t)
	o.cpuFences = true
	o.clientFenceReady(f) // builds the poll closure
	if n := testing.AllocsPerRun(100, func() { o.clientFenceReady(f) }); n != 0 {
		t.Fatalf("%v allocations per poll", n)
	}
}

// With CPU waits a client buffer whose fence is pending is composed, on the
// primary plane and on the overlay alike: neither plane waits for it. Once
// the fence signalled the same frame uses the plane.
func TestPendingClientFenceComposes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason func(o *Output) string
		// decide returns whether the plane took the buffer.
		decide func(t *testing.T, o *Output, k *mockkms, fence *os.File) bool
	}{
		{"primary scanout", func(o *Output) string { return o.reason }, func(t *testing.T, o *Output, k *mockkms, fence *os.File) bool {
			o.primary.formats = []domain.DMABufFormat{{Format: fourccXRGB}}
			s, c := fullscreenScene()
			content := c[1]
			content.Acquire = fence
			fb, _, _ := o.scanoutFrame(s, map[domain.WindowID]ports.SurfaceContent{1: content})
			return fb != 0
		}},
		{"overlay", func(o *Output) string { return o.overlayReason }, func(t *testing.T, o *Output, k *mockkms, fence *os.File) bool {
			o.overlay.formats = []domain.DMABufFormat{{Format: fourccXRGB}}
			s, c := overlayScene()
			content := c[2]
			content.Acquire = fence
			ov, _ := o.overlayFrame(s, map[domain.WindowID]ports.SurfaceContent{2: content})
			ov.close()
			return ov.fb != 0
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, k, _ := overlayOutput(t)
			k.EXPECT().addFB(mock.Anything, mock.Anything).Return(60, nil).Maybe()
			o.cpuFences = true
			fence, w := pipeFence(t)
			if tc.decide(t, o, k, fence) || tc.reason(o) != "fence_pending" {
				t.Fatalf("pending fence: plane took the buffer or reason %q", tc.reason(o))
			}
			_, _ = w.Write([]byte{1})
			if !tc.decide(t, o, k, fence) {
				t.Fatalf("signalled fence: reason %q", tc.reason(o))
			}
		})
	}
}

// A commit that is retried (here: VRR refused, then the same frame without
// it) waits for its fence once, not once per attempt.
func TestCommitWaitsForItsFenceOnce(t *testing.T) {
	o, _, commits := testOutput(t, unix.EINVAL)
	o.cpuFences = true
	o.fenceLimit = time.Millisecond
	fence, _ := pipeFence(t)
	if err := o.commitFrame(70, fence, false, true, pendingFrame{}); err != nil {
		t.Fatal(err)
	}
	if len(*commits) < 2 {
		t.Fatalf("%d commits: no retry happened", len(*commits))
	}
	if o.flipStats.fenceTimeouts != 1 {
		t.Fatalf("%d fence waits for %d commits, want one", o.flipStats.fenceTimeouts, len(*commits))
	}
	for i, c := range *commits {
		if _, ok := c.req.value(tPrimary, pFence); ok {
			t.Fatalf("commit %d carries IN_FENCE_FD with CPU waits", i)
		}
	}
}

func TestWaitNilFencesAllocations(t *testing.T) {
	o := &Output{cpuFences: true}
	if n := testing.AllocsPerRun(100, func() { o.waitFences(nil, nil, nil) }); n != 0 {
		t.Fatalf("nil fence wait allocated %g times", n)
	}
}
