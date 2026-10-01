package core

import (
	"context"
	"testing"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
	"github.com/stretchr/testify/mock"
)

// indicatorClock is a clock the test moves; its timers never fire by
// themselves: the test calls captureFlashTick, and the durations asked for
// are recorded.
type indicatorClock struct {
	now    time.Time
	timers []time.Duration
}

func indicatorCore(t *testing.T) (*Core, *indicatorClock) {
	t.Helper()
	ic := &indicatorClock{now: time.Unix(1000, 0)}
	clock := portsmocks.NewMockClock(t)
	clock.EXPECT().Now().RunAndReturn(func() time.Time { return ic.now }).Maybe()
	clock.EXPECT().NewTimer(mock.Anything).RunAndReturn(func(d time.Duration) ports.Timer {
		ic.timers = append(ic.timers, d)
		timer := portsmocks.NewMockTimer(t)
		timer.EXPECT().C().Return(make(chan time.Time)).Maybe()
		timer.EXPECT().Stop().Return(true).Maybe()
		return timer
	}).Maybe()
	var cfg ports.Config
	cfg.Keyboard.CmdKey = "super"
	cfg.Layout.MaxColumns = 2
	cfg.Border.Width = 0
	c, err := New(cfg, Channels{Scenes: make(chan []ports.Scene, 1), Commands: make(chan ports.ClientCommand, 64), Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	c.addScreen(ports.OutputInfo{Name: "A", Width: 300, Height: 200})
	return c, ic
}

func indicatorScene(t *testing.T, c *Core) ports.Scene {
	t.Helper()
	if err := c.publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	return (<-c.ch.Scenes)[0]
}

func TestCaptureIndicatorFlashLastsOneSecond(t *testing.T) {
	c, ic := indicatorCore(t)
	if s := indicatorScene(t, c); s.CaptureIndicators != nil {
		t.Fatalf("indicator without capture: %+v", s.CaptureIndicators)
	}
	// A one-shot screenshot of the output, by any protocol.
	if !c.captureFrame(ports.CaptureFrameTaken{Output: "A"}) {
		t.Fatal("first frame shows nothing new")
	}
	want := []ports.CaptureIndicator{{Rect: Rect{W: 300, H: 200}}}
	if s := indicatorScene(t, c); len(s.CaptureIndicators) != 1 || s.CaptureIndicators[0] != want[0] {
		t.Fatalf("indicators %+v", s.CaptureIndicators)
	}
	if len(ic.timers) != 1 || ic.timers[0] != ports.CaptureFlash {
		t.Fatalf("timers %v, want one of %v", ic.timers, ports.CaptureFlash)
	}
	// A frame later extends the flash without a new scene.
	ic.now = ic.now.Add(600 * time.Millisecond)
	if c.captureFrame(ports.CaptureFrameTaken{Output: "A"}) {
		t.Fatal("extending a flash asks for a scene")
	}
	// The first timer fires at its due time: the flash was extended, it stays.
	ic.now = ic.now.Add(400 * time.Millisecond)
	if c.captureFlashTick() {
		t.Fatal("flash ended early")
	}
	if s := indicatorScene(t, c); len(s.CaptureIndicators) != 1 {
		t.Fatalf("indicators %+v", s.CaptureIndicators)
	}
	// Just before the end it stays; at the end it goes.
	ic.now = ic.now.Add(599 * time.Millisecond)
	if s := indicatorScene(t, c); len(s.CaptureIndicators) != 1 {
		t.Fatal("flash shorter than one second after the last frame")
	}
	ic.now = ic.now.Add(time.Millisecond)
	if !c.captureFlashTick() {
		t.Fatal("flash did not end")
	}
	if s := indicatorScene(t, c); s.CaptureIndicators != nil {
		t.Fatalf("indicators %+v after the flash", s.CaptureIndicators)
	}
	if c.capt.timerStop != nil || len(c.capt.flashes) != 0 {
		t.Fatal("timer or flash left")
	}
}

// Engaging protection clears a live flash and its timer, so no indicator is
// shown for a capture that can no longer happen, and none comes back after the
// unlock; capture flashes then work again.
func TestCaptureIndicatorFlashClearedByProtectionAndWorksAfterUnlock(t *testing.T) {
	c, ic := indicatorCore(t)
	state := ports.SecurityState{Generation: 1}
	gate := portsmocks.NewMockSessionSecurity(t)
	gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState { return state }).Maybe()
	c.ch.Security = gate
	if !c.captureFrame(ports.CaptureFrameTaken{Output: "A"}) {
		t.Fatal("first frame shows nothing new")
	}
	if len(c.capt.flashes) != 1 || c.capt.timerStop == nil {
		t.Fatalf("flash %d, timer armed %v", len(c.capt.flashes), c.capt.timerStop != nil)
	}
	state = ports.SecurityState{Generation: 2, Protected: true}
	if !c.syncSecurity() {
		t.Fatal("protection not engaged")
	}
	if len(c.capt.flashes) != 0 || c.capt.timerStop != nil || c.capt.timerC != nil {
		t.Fatalf("flash %d, timer %v left under protection", len(c.capt.flashes), c.capt.timerStop != nil)
	}
	if s := indicatorScene(t, c); s.CaptureIndicators != nil {
		t.Fatalf("indicators %+v while protected", s.CaptureIndicators)
	}
	// The clock passes the old deadline: nothing comes back after the unlock.
	ic.now = ic.now.Add(2 * time.Second)
	state = ports.SecurityState{Generation: 3}
	if !c.syncSecurity() {
		t.Fatal("protection not released")
	}
	if s := indicatorScene(t, c); s.CaptureIndicators != nil {
		t.Fatalf("stale indicators %+v after the unlock", s.CaptureIndicators)
	}
	timers := len(ic.timers)
	if !c.captureFrame(ports.CaptureFrameTaken{Output: "A"}) {
		t.Fatal("first frame after the unlock shows nothing new")
	}
	if s := indicatorScene(t, c); len(s.CaptureIndicators) != 1 {
		t.Fatalf("indicators %+v after the unlock", s.CaptureIndicators)
	}
	if len(ic.timers) != timers+1 || c.capt.timerStop == nil {
		t.Fatalf("timer not re-armed after the unlock: %v", ic.timers)
	}
	ic.now = ic.now.Add(ports.CaptureFlash)
	if !c.captureFlashTick() {
		t.Fatal("flash did not end after the unlock")
	}
}

func TestCaptureIndicatorRegionAndUnion(t *testing.T) {
	c, _ := indicatorCore(t)
	c.captureFrame(ports.CaptureFrameTaken{Output: "A", Region: Rect{X: 250, Y: 150, W: 100, H: 100}})
	c.captureFrame(ports.CaptureFrameTaken{Output: "A", Region: Rect{X: 250, Y: 150, W: 100, H: 100}})
	c.captureFrame(ports.CaptureFrameTaken{Output: "A"})
	s := indicatorScene(t, c)
	if len(s.CaptureIndicators) != 2 || s.CaptureIndicators[0].Rect != (Rect{X: 250, Y: 150, W: 50, H: 50}) || s.CaptureIndicators[1].Rect != (Rect{W: 300, H: 200}) {
		t.Fatalf("indicators %+v", s.CaptureIndicators)
	}
	// A target with nothing on screen marks nothing and stores nothing.
	c.captureFrame(ports.CaptureFrameTaken{Output: "gone"})
	c.captureFrame(ports.CaptureFrameTaken{Output: "A", Region: Rect{X: 900, Y: 900, W: 5, H: 5}})
	if len(c.capt.flashes) != 2 {
		t.Fatalf("flashes %+v", c.capt.flashes)
	}
}

func TestCaptureIndicatorRecordingLivesWithItsSession(t *testing.T) {
	c, ic := indicatorCore(t)
	c.captureOpen(ports.CaptureSessionOpen{ID: 1, Output: "A", Region: Rect{X: 10, Y: 10, W: 50, H: 40}})
	if s := indicatorScene(t, c); s.CaptureIndicators != nil {
		t.Fatalf("indicator of a session that captured nothing: %+v", s.CaptureIndicators)
	}
	if !c.captureFrame(ports.CaptureFrameTaken{Session: 1, Output: "A", Region: Rect{X: 10, Y: 10, W: 50, H: 40}}) {
		t.Fatal("first frame of a session shows nothing new")
	}
	// Long after the flash, the session still shows.
	ic.now = ic.now.Add(time.Hour)
	c.captureFlashTick()
	s := indicatorScene(t, c)
	if len(s.CaptureIndicators) != 1 || s.CaptureIndicators[0].Rect != (Rect{X: 10, Y: 10, W: 50, H: 40}) {
		t.Fatalf("indicators %+v", s.CaptureIndicators)
	}
	c.captureClose(1)
	if s := indicatorScene(t, c); s.CaptureIndicators != nil {
		t.Fatalf("indicators %+v after the session", s.CaptureIndicators)
	}
}

func TestCaptureIndicatorHiddenWorkspacePill(t *testing.T) {
	c, ws, _ := hiddenCaptureCommands(t)
	ic := &indicatorClock{now: time.Unix(5, 0)}
	clock := portsmocks.NewMockClock(t)
	clock.EXPECT().Now().RunAndReturn(func() time.Time { return ic.now }).Maybe()
	timer := portsmocks.NewMockTimer(t)
	timer.EXPECT().C().Return(make(chan time.Time)).Maybe()
	timer.EXPECT().Stop().Return(true).Maybe()
	clock.EXPECT().NewTimer(mock.Anything).Return(timer).Maybe()
	c.ch.Clock = clock
	c.configures.cw.reset()
	c.captureOpen(ports.CaptureSessionOpen{ID: 1, Workspace: ws.ID})
	c.captureFrame(ports.CaptureFrameTaken{Session: 1, Workspace: ws.ID})
	s := indicatorScene(t, c)
	pill := ports.CaptureIndicator{Pill: true, Rect: Rect{X: 300 - ports.CapturePillInset - ports.CapturePillSize, Y: ports.CapturePillInset, W: ports.CapturePillSize, H: ports.CapturePillSize}}
	if len(s.CaptureIndicators) != 1 || s.CaptureIndicators[0] != pill {
		t.Fatalf("indicators %+v, want %+v", s.CaptureIndicators, pill)
	}
	if s.CaptureScene == nil || s.CaptureScene.CaptureIndicators != nil || s.CaptureScene.Capture != nil {
		t.Fatalf("the off-screen workspace scene carries an indicator: %+v", s.CaptureScene)
	}
	// On screen, the same session shows a border on its frame instead.
	c.screens[0].mon.show(ws)
	if c.screens[0].mon.Current() != ws {
		t.Fatal("workspace not on screen")
	}
	s = indicatorScene(t, c)
	if len(s.CaptureIndicators) != 1 || s.CaptureIndicators[0].Pill || s.CaptureIndicators[0].Rect != ws.Output {
		t.Fatalf("indicators %+v", s.CaptureIndicators)
	}
}

// With nothing captured the indicator costs nothing in core.
func TestCaptureIndicatorNoAllocationWhenIdle(t *testing.T) {
	c, _ := indicatorCore(t)
	sc := c.screens[0]
	if n := testing.AllocsPerRun(100, func() {
		c.captureExpire()
		c.armCaptureTimer()
		if c.captureIndicators(sc) != nil {
			t.Fatal("indicators")
		}
	}); n != 0 {
		t.Fatalf("%v allocations with no capture", n)
	}
	// A live flash, steady state, allocates nothing either (measured on the
	// system clock: the generated clock mock allocates on every call).
	c.ch.Clock = nil
	c.captureFrame(ports.CaptureFrameTaken{Output: "A"})
	if n := testing.AllocsPerRun(100, func() { c.captureExpire(); c.armCaptureTimer() }); n != 0 {
		t.Fatalf("%v allocations keeping a flash", n)
	}
}

// A target core cannot locate (the workspace or output is gone) has no mark
// to show: core marks nothing, and the output owner fails such a capture
// after its bounded wait rather than serving it without an indicator.
func TestCaptureIndicatorMissingForAnUnresolvableTarget(t *testing.T) {
	c, _ := indicatorCore(t)
	for _, v := range []ports.CaptureFrameTaken{{Output: "GONE"}, {Workspace: 999}, {Output: "A", Region: Rect{X: 900, Y: 900, W: 10, H: 10}}} {
		if c.captureFrame(v) {
			t.Fatalf("%+v marked", v)
		}
	}
	if s := indicatorScene(t, c); s.CaptureIndicators != nil {
		t.Fatalf("indicators %+v for a target that is not there", s.CaptureIndicators)
	}
}

// A target thinner than the border would have nothing to draw: core lists a
// mark at least ports.MinCaptureMark wide and tall around it, inside the
// output; an output smaller than that is marked whole.
func TestCaptureIndicatorInflatesThinTargets(t *testing.T) {
	c, _ := indicatorCore(t)
	for _, tc := range []struct {
		region, want Rect
	}{
		{Rect{X: 100, Y: 50, W: 1, H: 100}, Rect{X: 98, Y: 50, W: 5, H: 100}},
		{Rect{X: 100, Y: 50, W: 100, H: 1}, Rect{X: 100, Y: 48, W: 100, H: 5}},
		{Rect{X: 100, Y: 50, W: 1, H: 1}, Rect{X: 98, Y: 48, W: 5, H: 5}},
		{Rect{X: 0, Y: 0, W: 1, H: 1}, Rect{X: 0, Y: 0, W: 5, H: 5}},
		{Rect{X: 299, Y: 199, W: 1, H: 1}, Rect{X: 295, Y: 195, W: 5, H: 5}},
		{Rect{X: 100, Y: 50, W: 4, H: 4}, Rect{X: 100, Y: 50, W: 5, H: 5}},
		{Rect{X: 100, Y: 50, W: 5, H: 5}, Rect{X: 100, Y: 50, W: 5, H: 5}},
	} {
		c.capt.flashes = nil
		c.captureFrame(ports.CaptureFrameTaken{Output: "A", Region: tc.region})
		s := indicatorScene(t, c)
		if len(s.CaptureIndicators) != 1 || s.CaptureIndicators[0].Rect != tc.want {
			t.Fatalf("region %+v: marks %+v, want %+v", tc.region, s.CaptureIndicators, tc.want)
		}
		m := s.CaptureIndicators[0]
		if !m.Drawable() {
			t.Fatalf("region %+v: mark %+v draws nothing", tc.region, m)
		}
		if m.Rect.X > tc.region.X || m.Rect.Y > tc.region.Y || m.Rect.X+m.Rect.W < tc.region.X+tc.region.W || m.Rect.Y+m.Rect.H < tc.region.Y+tc.region.H {
			t.Fatalf("region %+v not inside its mark %+v", tc.region, m.Rect)
		}
	}
	// An output smaller than the minimum is marked whole.
	if got := thickenMark(Rect{W: 1, H: 1}, Rect{W: 3, H: 4}); got != (Rect{W: 3, H: 4}) {
		t.Fatalf("tiny output mark %+v", got)
	}
}

// A recording session that keeps being published allocates nothing for its
// capture state: the marks, the scene capture and the session states are
// scratch or shared while unchanged.
func TestCaptureRecordingPublishAllocations(t *testing.T) {
	c, _ := indicatorCore(t)
	c.ch.Clock = nil
	cmds := make(chan ports.ClientCommand, 64)
	c.ch.Commands = cmds
	c.captureOpen(ports.CaptureSessionOpen{ID: 1, Output: "A", Region: Rect{X: 10, Y: 10, W: 50, H: 40}})
	c.captureOpen(ports.CaptureSessionOpen{ID: 2, Output: "A"})
	for id := uint64(1); id <= 2; id++ {
		c.captureFrame(ports.CaptureFrameTaken{Session: id, Output: "A"})
	}
	ctx := context.Background()
	sc := c.screens[0]
	step := func() {
		v, err := c.captureEvaluate(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if c.captureSceneFor(sc, v) == nil || len(c.captureIndicators(sc)) == 0 {
			t.Fatal("no capture state")
		}
	}
	step()
	for len(cmds) > 0 {
		<-cmds
	}
	first := sc.capMarks
	n := testing.AllocsPerRun(100, step)
	t.Logf("capture state allocations per recording publish: %v", n)
	if n != 0 {
		t.Fatalf("%v allocations per recording publish", n)
	}
	if len(cmds) != 0 {
		t.Fatal("steady publish sent a command")
	}
	if &first[0] != &sc.capMarks[0] {
		t.Fatal("unchanged marks were cloned")
	}
}

// The pill of a workspace rendered for capture only lies whole inside its
// output; an output too small for it (<= 8 logical px tall, < 12 wide) is
// filled whole instead, never cut to nothing.
func TestCaptureIndicatorPillFitsTheOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  Rect
		want Rect
	}{
		{"regular", Rect{W: 300, H: 200}, Rect{X: 280, Y: 8, W: 12, H: 12}},
		{"short", Rect{W: 300, H: 8}, Rect{W: 300, H: 8}},
		{"narrow", Rect{W: 11, H: 200}, Rect{W: 11, H: 200}},
		{"tiny", Rect{W: 5, H: 6}, Rect{W: 5, H: 6}},
		{"just fits", Rect{W: 20, H: 20}, Rect{X: 0, Y: 8, W: 12, H: 12}},
	} {
		got := pillRect(tc.out)
		if got != tc.want {
			t.Fatalf("%s: pill %+v, want %+v", tc.name, got, tc.want)
		}
		m := ports.CaptureIndicator{Pill: true, Rect: got}
		if !m.DrawableIn(tc.out.W, tc.out.H) {
			t.Fatalf("%s: pill %+v not drawable in %+v", tc.name, got, tc.out)
		}
	}
	// A hidden workspace on a short output lists the clamped pill.
	c, ws, _ := hiddenCaptureCommands(t)
	c.screens[0].mon.SetOutput(300, 8)
	c.configures.cw.reset()
	c.captureOpen(ports.CaptureSessionOpen{ID: 1, Workspace: ws.ID})
	c.captureFrame(ports.CaptureFrameTaken{Session: 1, Workspace: ws.ID})
	s := indicatorScene(t, c)
	if len(s.CaptureIndicators) != 1 || !s.CaptureIndicators[0].Pill || s.CaptureIndicators[0].Rect != (Rect{W: 300, H: 8}) {
		t.Fatalf("indicators %+v", s.CaptureIndicators)
	}
}
