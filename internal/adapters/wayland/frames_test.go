package wayland

import (
	"slices"
	"testing"
	"time"

	"github.com/bnema/go-wayland-bindings/server/wayland"
	"github.com/bnema/neferwl/internal/ports"
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

func TestInvisibleFramePacingAndMigration(t *testing.T) {
	out := &output{place: ports.OutputPlacement{Info: ports.OutputInfo{Name: "DP-2", RefreshMilli: 60000}}}
	s := &Server{outputs: []*output{out}, awaiting: map[string][]*wayland.Callback{}, frameDue: map[string]time.Time{}, frameReady: make(chan struct{}, 1)}
	w := &window{hasLast: true, last: ports.ConfigureWindow{Output: "DP-2", Visible: true}}
	root := &surface{xdg: &xdgSurface{window: w}}
	child := &surface{sub: subState{parent: root}}
	cursor := &surface{}
	// Use distinct live callback pointers as identities; dueFrames never dereferences them.
	a, b := new(wayland.Callback), new(wayland.Callback)
	s.queueFrames(child, []*wayland.Callback{a})
	s.queueFrames(cursor, []*wayland.Callback{b})
	w.last.Visible = false
	s.relocateCallbacks(root)
	if len(s.awaiting[suspendedFrameQueue]) != 1 || len(s.awaiting["DP-2"]) != 0 || len(s.awaiting[""]) != 1 || s.frameOutput(child) != suspendedFrameQueue {
		t.Fatalf("migration: %+v", s.awaiting)
	}
	now := time.Unix(100, 0)
	fire, _, _ := s.dueFrames(now, nil)
	if !slices.Contains(fire, suspendedFrameQueue) || !slices.Contains(fire, "") {
		t.Fatalf("initial fire: %v", fire)
	}
	delete(s.awaiting, suspendedFrameQueue)
	delete(s.awaiting, "")
	s.queueFrames(child, []*wayland.Callback{a})
	s.queueFrames(cursor, []*wayland.Callback{b})
	fire, wait, _ := s.dueFrames(now.Add(20*time.Millisecond), nil)
	if slices.Contains(fire, suspendedFrameQueue) || !slices.Contains(fire, "") || wait != 980*time.Millisecond {
		t.Fatalf("20ms: fire %v wait %v", fire, wait)
	}
	w.last.Visible = true
	s.relocateCallbacks(root)
	fire, _, _ = s.dueFrames(now.Add(21*time.Millisecond), nil)
	if !slices.Contains(fire, "DP-2") || len(s.awaiting[suspendedFrameQueue]) != 0 {
		t.Fatalf("resume: fire %v queues %+v", fire, s.awaiting)
	}
}

// A window only a capture session draws is physically invisible (no scanout,
// no presentation feedback) but is not throttled: its callbacks stay on its
// output's queue and move when Captured changes.
func TestCapturedWindowKeepsOutputFramePacing(t *testing.T) {
	out := &output{place: ports.OutputPlacement{Info: ports.OutputInfo{Name: "DP-2", RefreshMilli: 60000}}}
	s := &Server{outputs: []*output{out}, awaiting: map[string][]*wayland.Callback{}, frameDue: map[string]time.Time{}, frameReady: make(chan struct{}, 1)}
	w := &window{hasLast: true, last: ports.ConfigureWindow{Output: "DP-2", Visible: true}}
	root := &surface{xdg: &xdgSurface{window: w}}
	a := new(wayland.Callback)
	s.queueFrames(root, []*wayland.Callback{a})
	w.last = ports.ConfigureWindow{Output: "DP-2", Captured: true}
	s.relocateCallbacks(root)
	if !s.invisible(root) || s.suspended(root) || s.frameOutput(root) != "DP-2" || len(s.awaiting["DP-2"]) != 1 || len(s.awaiting[suspendedFrameQueue]) != 0 {
		t.Fatalf("captured: invisible %v suspended %v queues %+v", s.invisible(root), s.suspended(root), s.awaiting)
	}
	w.last = ports.ConfigureWindow{Output: "DP-2"}
	s.relocateCallbacks(root)
	if !s.suspended(root) || len(s.awaiting[suspendedFrameQueue]) != 1 || len(s.awaiting["DP-2"]) != 0 {
		t.Fatalf("released: queues %+v", s.awaiting)
	}
}
