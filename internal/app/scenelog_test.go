package app

import (
	"strings"
	"testing"

	"github.com/bnema/neferwl/internal/ports"
	"github.com/bnema/zerowrap"
)

func logTestScenes() []ports.Scene {
	return []ports.Scene{{
		Output: "A", Seq: 7, OutputWidth: 1920, OutputHeight: 1080, Scale: 1,
		Windows: []ports.SceneWindow{
			{ID: 1, Rect: ports.Rect{X: 10, Y: 20, W: 300, H: 200}},
			{ID: 2, Rect: ports.Rect{X: 5, Y: 6, W: 7, H: 8}, FocusEffect: 0.5},
		},
	}, {Output: "B"}}
}

// With debug off the scene log builds nothing: no rect strings, no event.
// Measured before the level check: 3 allocations per run for two scenes.
func TestLogScenesAllocations(t *testing.T) {
	out := &lockedBuffer{}
	log := zerowrap.New(zerowrap.Config{Format: "json", Level: "info", Output: out})
	set := logTestScenes()
	if got := testing.AllocsPerRun(100, func() { logScenes(log, set) }); got != 0 {
		t.Fatalf("logScenes at info level: %g allocations per run, want 0", got)
	}
	if out.String() != "" {
		t.Fatalf("info level logged: %s", out.String())
	}
}

func TestLogScenesDebug(t *testing.T) {
	out := &lockedBuffer{}
	log := zerowrap.New(zerowrap.Config{Format: "json", Level: "debug", Output: out})
	logScenes(log, logTestScenes())
	got := out.String()
	for _, want := range []string{
		`"output":"A"`, `"seq":7`, `"out_w":1920`, `"out_h":1080`, `"scale":1`,
		`"windows":["1:10,20 300x200","2:5,6 7x8"]`,
		`"focus_effect_window":2`, `"focus_effect":0.5`, `"message":"scene"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("debug log lacks %s: %s", want, got)
		}
	}
	if n := strings.Count(got, `"message":"scene"`); n != 2 {
		t.Fatalf("%d scene entries, want 2: %s", n, got)
	}
}
