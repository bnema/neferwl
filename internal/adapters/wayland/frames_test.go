package wayland

import (
	"testing"
	"time"

	"github.com/bnema/nefertty/internal/ports"
)

func TestFramePeriod(t *testing.T) {
	for _, tc := range []struct {
		milli int
		want  time.Duration
	}{{0, time.Second / 60}, {60000, time.Second / 60}, {165058, 6058476}, {144000, 6944444}} {
		if got := framePeriod(tc.milli); got != tc.want {
			t.Errorf("%d: %v want %v", tc.milli, got, tc.want)
		}
	}
}

// Each output is paced at its own refresh; a page flip resets its deadline.
func TestFireFramesPacing(t *testing.T) {
	fast := &output{place: ports.OutputPlacement{Info: ports.OutputInfo{Name: "DP-2", RefreshMilli: 165000}}}
	slow := &output{place: ports.OutputPlacement{Info: ports.OutputInfo{Name: "HDMI-A-1", RefreshMilli: 60000}}}
	s := &Server{outputs: []*output{fast, slow}, frameDue: map[string]time.Time{}}
	now := time.Unix(100, 0)
	if wait := s.fireFrames(now, "", false); wait != framePeriod(165000) {
		t.Fatalf("first wait %v", wait)
	}
	if due := s.frameDue["HDMI-A-1"]; due != now.Add(time.Second/60) {
		t.Fatalf("60 Hz due %v", due.Sub(now))
	}
	// A flip on DP-2 pushes its timer back: flips pace it.
	later := now.Add(time.Millisecond)
	s.fireFrames(later, "DP-2", true)
	if due := s.frameDue["DP-2"]; due != later.Add(framePeriod(165000)*3/2) {
		t.Fatalf("flip due %v", due.Sub(later))
	}
	// An unplugged output is forgotten.
	s.outputs = []*output{fast}
	s.fireFrames(later, "", false)
	if _, ok := s.frameDue["HDMI-A-1"]; ok {
		t.Fatal("stale output kept")
	}
}
