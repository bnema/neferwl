package wayland

import (
	"slices"
	"testing"
	"time"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/purego-libwayland/protocol/wayland"
)

func TestDueFramesAllocations(t *testing.T) {
	s := &Server{frameDue: map[string]time.Time{}, awaiting: map[string][]*wayland.Callback{}}
	now := time.Unix(100, 0)
	s.dueFrames(now, nil)
	if allocs := testing.AllocsPerRun(100, func() { s.dueFrames(now, nil) }); allocs != 0 {
		t.Errorf("idle dueFrames: %.1f allocs, want 0", allocs)
	} else {
		t.Logf("idle dueFrames: %.1f allocs", allocs)
	}
}

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

// Each output is paced at its own refresh; a flip fires its queue and
// pushes its deadline back; the pacer idles with nothing queued.
func TestDueFrames(t *testing.T) {
	fast := &output{place: ports.OutputPlacement{Info: ports.OutputInfo{Name: "DP-2", RefreshMilli: 165000}}}
	slow := &output{place: ports.OutputPlacement{Info: ports.OutputInfo{Name: "HDMI-A-1", RefreshMilli: 60000}}}
	s := &Server{outputs: []*output{fast, slow}, frameDue: map[string]time.Time{}, awaiting: map[string][]*wayland.Callback{}}
	// dueFrames only counts the queued callbacks: nil ones stand for them.
	queue := func(name string) { s.awaiting[name] = append(s.awaiting[name], nil) }
	fired := func(fire []string) {
		for _, name := range fire {
			delete(s.awaiting, name)
		}
	}
	now := time.Unix(100, 0)
	if _, _, idle := s.dueFrames(now, nil); !idle {
		t.Fatal("not idle without callbacks")
	}
	// A first callback goes out at once, then one per refresh.
	queue("DP-2")
	queue("HDMI-A-1")
	fire, _, idle := s.dueFrames(now, nil)
	if len(fire) != 2 || !idle {
		t.Fatalf("first fire %v idle %v", fire, idle)
	}
	fired(fire)
	queue("DP-2")
	queue("HDMI-A-1")
	fire, wait, _ := s.dueFrames(now.Add(time.Millisecond), nil)
	if len(fire) != 0 || wait != framePeriod(165000)-time.Millisecond {
		t.Fatalf("early fire %v wait %v", fire, wait)
	}
	at := now.Add(framePeriod(165000))
	fire, wait, _ = s.dueFrames(at, nil)
	if !slices.Equal(fire, []string{"DP-2"}) || wait != time.Second/60-framePeriod(165000) {
		t.Fatalf("165 Hz fire %v wait %v", fire, wait)
	}
	fired(fire)
	// A flip fires its output early and pushes its deadline to 1.5 periods.
	queue("DP-2")
	flip := at.Add(time.Millisecond)
	fire, _, _ = s.dueFrames(flip, map[string]bool{"DP-2": true})
	if !slices.Equal(fire, []string{"DP-2"}) || s.frameDue["DP-2"] != flip.Add(framePeriod(165000)*3/2) {
		t.Fatalf("flip fire %v due %v", fire, s.frameDue["DP-2"].Sub(flip))
	}
	fired(fire)
	// A queue on an unplugged output moves to the outputless one (60 Hz).
	s.outputs = []*output{fast}
	queue("HDMI-A-1")
	fire, _, _ = s.dueFrames(flip, nil)
	fired(fire)
	if _, ok := s.frameDue["HDMI-A-1"]; ok || len(s.awaiting["HDMI-A-1"]) != 0 || !slices.Equal(fire, []string{""}) {
		t.Fatalf("unplugged: fire %v due %v", fire, s.frameDue)
	}
}
