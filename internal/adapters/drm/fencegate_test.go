package drm

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
)

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
		{"560..3", [3]int{}, false},
	} {
		got, err := parseVersion(tc.in)
		if (err == nil) != tc.ok || tc.ok && got != tc.want {
			t.Errorf("parseVersion(%q) = %v, %v", tc.in, got, err)
		}
	}
}

func TestVersionLess(t *testing.T) {
	for _, tc := range []struct {
		a, b [3]int
		want bool
	}{
		{[3]int{560, 35, 2}, [3]int{560, 35, 3}, true},
		{[3]int{560, 35, 3}, [3]int{560, 35, 3}, false},
		{[3]int{560, 35, 5}, [3]int{560, 35, 3}, false},
		{[3]int{550, 120, 0}, [3]int{560, 35, 3}, true},
		{[3]int{560, 36, 0}, [3]int{560, 35, 3}, false},
		{[3]int{615, 78, 8}, [3]int{560, 35, 3}, false},
		{[3]int{470, 99, 99}, [3]int{560, 0, 0}, true},
	} {
		if got := versionLess(tc.a, tc.b); got != tc.want {
			t.Errorf("versionLess(%v, %v) = %v", tc.a, tc.b, got)
		}
	}
}

func TestBrokenInFence(t *testing.T) {
	for _, tc := range []struct {
		name, driver, version string
		err                   error
		want                  bool
	}{
		{"amd", "amdgpu", "", nil, false},
		{"intel", "i915", "garbage", nil, false},
		{"old nvidia", driverNVIDIA, "555.58.02", nil, true},
		{"just before", driverNVIDIA, "560.35.2", nil, true},
		{"fixed", driverNVIDIA, "560.35.03\n", nil, false},
		{"newer", driverNVIDIA, "615.78.08", nil, false},
		{"unreadable", driverNVIDIA, "", errors.New("no such file"), true},
		{"unparsable", driverNVIDIA, "n/a", nil, true},
	} {
		if got := brokenInFence(tc.driver, tc.version, tc.err); got != tc.want {
			t.Errorf("%s: brokenInFence = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// With CPU waits no commit carries IN_FENCE_FD; otherwise the plane does.
func TestFenceProp(t *testing.T) {
	f, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	defer w.Close()
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

// The CPU wait returns once the fence signals (a pipe stands in: readable
// means signalled) and gives up after fenceWaitLimit.
func TestWaitFences(t *testing.T) {
	o, _, _ := testOutput(t)
	o.cpuFences = true
	o.log = zerowrap.Default()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte{1})
	}()
	start := time.Now()
	o.waitFences(nil, r)
	if d := time.Since(start); d < 15*time.Millisecond || d > fenceWaitLimit/2 {
		t.Fatalf("waited %v for the fence", d)
	}
	w.Close()
	o.cpuFences = false
	o.waitFences(r) // not waited for: returns at once
}
