package core

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/bnema/neferwl/internal/ports"
)

func flashCore(t *testing.T) (*Core, *indicatorClock) {
	t.Helper()
	c, clock := indicatorCore(t)
	c.cfg.Animations.On = true
	c.cfg.Focus.Flash = true
	c.cfg.Focus.FlashWidth = 3
	c.cfg.Focus.FlashOpacity = 0.8
	c.cfg.Focus.FlashDuration = 400 * time.Millisecond
	c.cfg.Border.Active = "#abcdef"
	for id := WindowID(1); id <= 3; id++ {
		c.windows.mapped(ports.WindowMapped{ID: id}, clock.now.Add(-time.Minute))
		c.cur().mon.AddWindow(id)
	}
	return c, clock
}

func startFlash(t *testing.T, c *Core, clock *indicatorClock, level flashLevel, id WindowID) {
	t.Helper()
	c.requestFlash(level, id)
	clock.now = clock.now.Add(flashSettle)
	if !c.flashTick() {
		t.Fatal("flash did not start")
	}
}

func TestFlashFadeAndOutputFrames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock := flashCore(t)
		a := c.cur()
		c.addScreen(ports.OutputInfo{Name: "B", Width: 400, Height: 300})
		b := c.screens[1]
		startFlash(t, c, clock, flashWindow, 3)
		first := stepScenes(t, c, a)
		f := first["A"].Flash
		if f == nil || f.Alpha != 0.8 || f.Color != c.cfg.Border.Active || f.Width != 3 {
			t.Fatalf("flash %+v", f)
		}
		if first["B"].Flash != nil || !first["A"].Animating || !c.animating() {
			t.Fatal("output/animation routing")
		}
		clock.now = clock.now.Add(200 * time.Millisecond)
		other := stepScenes(t, c, b)
		if other["A"].Seq != first["A"].Seq || *other["A"].Flash != *f {
			t.Fatal("other output advanced flash")
		}
		half := stepScenes(t, c, a)
		if half["A"].Flash.Alpha != 0.4 {
			t.Fatalf("half alpha %v", half["A"].Flash.Alpha)
		}
		clock.now = clock.now.Add(200 * time.Millisecond)
		end := stepScenes(t, c, a)
		if end["A"].Flash != nil || end["A"].Animating || c.animating() {
			t.Fatal("flash did not stop frames")
		}
		if end["A"].Seq == half["A"].Seq {
			t.Fatal("end retained animating sequence")
		}

	})
}

func TestFlashDebounceCooldownAndReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock := flashCore(t)
		c.requestFlash(flashWindow, 1)
		clock.now = clock.now.Add(100 * time.Millisecond)
		c.requestFlash(flashWindow, 2)
		clock.now = clock.now.Add(50 * time.Millisecond)
		if c.flashTick() || c.flash.pending == nil {
			t.Fatal("early stale timer started flash")
		}
		clock.now = clock.now.Add(100 * time.Millisecond)
		if !c.flashTick() || c.flash.running.window != 2 {
			t.Fatal("last request did not win")
		}
		c.requestFlash(flashWindow, 2)
		if c.flash.running != nil {
			t.Fatal("request did not replace running flash")
		}
		clock.now = clock.now.Add(flashSettle)
		if c.flashTick() {
			t.Fatal("cooldown ignored")
		}
		clock.now = clock.now.Add(time.Second)
		startFlash(t, c, clock, flashWindow, 2)
		c.requestFlash(flashColumn, 2)
		clock.now = clock.now.Add(flashSettle)
		if !c.flashTick() || c.flash.running.level != flashColumn {
			t.Fatal("level not part of cooldown key")
		}

	})
}

func TestFlashSettlesAndResolvesFinalRect(t *testing.T) {
	for _, level := range []flashLevel{flashWindow, flashColumn} {
		t.Run(string(rune('0'+level)), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, clock := flashCore(t)
				w := c.cur().mon.Current()
				c.requestFlash(level, 3)
				w.view.Off = 20
				clock.now = clock.now.Add(flashSettle)
				if c.flashTick() || c.flash.timerC == nil {
					t.Fatal("did not wait for layout")
				}
				w.view.Stop()
				w.MoveColumn(-1)
				clock.now = clock.now.Add(flashRecheck)
				if !c.flashTick() {
					t.Fatal("did not start after settling")
				}
				want := Rect{W: 300, H: 200}
				for _, p := range c.cur().mon.Layout() {
					if p.ID == 3 {
						want = p.Rect
					}
				}

				if c.flash.rect != want {
					t.Fatalf("rect %+v want %+v", c.flash.rect, want)
				}

			})
		})
	}
}

func TestFlashSkipsIneligibleTargets(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Core, *indicatorClock)
	}{
		{"disabled", func(c *Core, _ *indicatorClock) { c.cfg.Focus.Flash = false }},
		{"animations off", func(c *Core, _ *indicatorClock) { c.cfg.Animations.On = false }},
		{"protected", func(c *Core, _ *indicatorClock) { c.security.Protected = true }},
		{"overview", func(c *Core, _ *indicatorClock) { c.cur().mon.ToggleOverview() }},
		{"fullscreen", func(c *Core, _ *indicatorClock) { c.cur().mon.ToggleFullscreen() }},
		{"gone", func(c *Core, _ *indicatorClock) { c.cur().mon.RemoveWindow(3) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, clock := flashCore(t)
				test.change(c, clock)
				c.requestFlash(flashWindow, 3)
				clock.now = clock.now.Add(flashSettle)
				if c.flashTick() || c.flash.running != nil {
					t.Fatal("ineligible flash")
				}

			})
		})
	}
}

func TestFlashStopAndColor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock := flashCore(t)
		c.cfg.Focus.FlashColor = "#123456"
		c.cfg.Animations.Slowdown = 10
		startFlash(t, c, clock, flashWindow, 3)
		if s := indicatorScene(t, c); s.Flash.Color != "#123456" {
			t.Fatal("configured color ignored")
		}
		clock.now = clock.now.Add(c.cfg.Focus.FlashDuration)
		if indicatorScene(t, c).Flash != nil {
			t.Fatal("duration scaled by slowdown")
		}
		c.requestFlash(flashWindow, 2)
		c.stopFlash()
		if c.flash.timerC != nil || c.flash.pending != nil || c.flash.running != nil {
			t.Fatal("stop retained state")
		}

	})
}

func TestFlashActionTriggers(t *testing.T) {
	for _, test := range []struct {
		action Action
		level  flashLevel
		setup  func(*Core)
	}{
		{ActionFocusColumnLeft, flashColumn, nil},
		{ActionFocusColumnRight, flashColumn, func(c *Core) { c.cur().mon.Current().FocusID(1) }},
		{ActionMoveColumnLeft, flashColumn, nil},
		{ActionMoveColumnRight, flashColumn, func(c *Core) { c.cur().mon.Current().FocusID(1) }},
		{ActionConsumeOrExpelLeft, flashWindow, nil},
		{ActionConsumeOrExpelRight, flashWindow, func(c *Core) { c.cur().mon.Current().FocusID(1) }},
		{ActionToggleFloating, flashWindow, nil},
		{ActionFocusWindowUp, flashWindow, func(c *Core) { c.cur().mon.Current().ConsumeOrExpel(-1) }},
		{ActionFocusWindowDown, flashWindow, func(c *Core) { w := c.cur().mon.Current(); w.ConsumeOrExpel(-1); w.FocusWindow(-1) }},
		{ActionMoveWindowUp, flashWindow, func(c *Core) { c.cur().mon.Current().ConsumeOrExpel(-1) }},
		{ActionMoveWindowDown, flashWindow, func(c *Core) { w := c.cur().mon.Current(); w.ConsumeOrExpel(-1); w.FocusWindow(-1) }},
		{ActionToggleWindowStash, flashWindow, func(c *Core) { c.cur().mon.Current().ToggleWindowStash() }},
		{ActionToggleStashVisible, flashWindow, func(c *Core) { w := c.cur().mon.Current(); w.ToggleWindowStash(); w.ToggleStashVisible() }},
		{ActionToggleOverview, flashWindow, func(c *Core) { c.cur().mon.ToggleOverview() }},
	} {
		t.Run(string(test.action), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, clock := flashCore(t)
				if test.setup != nil {
					test.setup(c)
				}
				c.applyAction(test.action)
				if c.flash.pending == nil || c.flash.pending.level != test.level {
					t.Fatalf("pending %+v want level %v", c.flash.pending, test.level)
				}
				target := *c.flash.pending
				clock.now = clock.now.Add(flashSettle)
				if !c.flashTick() {
					t.Fatal("action flash did not start")
				}
				var want Rect
				for _, p := range c.cur().mon.Layout() {
					if p.ID == target.window {
						want = p.Rect
					}
				}
				if c.flash.rect != want {
					t.Fatalf("action rect %+v want %+v", c.flash.rect, want)
				}

			})
		})
	}
}

func TestFlashExcludedActions(t *testing.T) {
	for _, action := range []Action{ActionMoveWindowToWorkspaceNext, ActionMoveColumnToWorkspaceNext, ActionToggleFullscreen, ActionMaximizeColumn, ActionCycleColumnWidth, ActionFocusColumnRight, ActionMoveColumnRight, ActionMoveWindowUp} {
		t.Run(string(action), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, _ := flashCore(t)
				c.applyAction(action)
				if c.flash.pending != nil {
					t.Fatalf("unexpected flash %+v", c.flash.pending)
				}

			})
		})
	}
}

func TestFlashCrossOutputActionTriggers(t *testing.T) {
	for _, action := range []Action{ActionFocusMonitorLeft, ActionFocusMonitorRight, ActionFocusMonitorUp, ActionFocusMonitorDown, ActionMoveWorkspaceLeft, ActionMoveWorkspaceRight, ActionMoveWorkspaceToMonitorUp, ActionMoveWorkspaceToMonitorDown} {
		t.Run(string(action), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, clock := flashCore(t)
				c.addScreen(ports.OutputInfo{Name: "B", Width: 400, Height: 300})
				a, b := c.screens[0], c.screens[1]
				a.x, a.y = 0, 0
				b.x, b.y = 300, 0
				switch monitorDirection(action) {
				case dirLeft:
					b.x = -400
				case dirUp:
					b.x, b.y = 0, -300
				case dirDown:
					b.x, b.y = 0, 200
				}
				c.applyAction(action)
				id, _ := b.mon.Focused()
				if id == 0 {
					if c.flash.pending != nil {
						t.Fatalf("empty destination request %+v", c.flash.pending)
					}
					return
				}
				if c.flash.pending == nil || *c.flash.pending != (flashTarget{level: flashWindow, window: id, output: "B"}) {
					t.Fatalf("destination request %+v", c.flash.pending)
				}
				clock.now = clock.now.Add(flashSettle)
				if !c.flashTick() {
					t.Fatal("destination window flash did not start")
				}
				want, _, ok := c.flashRect(*c.flash.running, b)
				if !ok || c.flash.rect != want {
					t.Fatalf("window rect %+v want %+v", c.flash.rect, want)
				}

			})
		})
	}
}

func TestFlashColumnUnion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock := flashCore(t)
		w := c.cur().mon.Current()
		w.ConsumeOrExpel(-1)
		startFlash(t, c, clock, flashColumn, 3)
		col := w.Columns[w.columnOf(3)]
		var area int
		for _, p := range c.cur().mon.Layout() {
			for _, id := range col.Windows {
				if id == p.ID {
					area += p.Rect.W * p.Rect.H
				}
			}
		}
		if c.flash.rect.W*c.flash.rect.H != area {
			t.Fatalf("union %+v does not cover column area %d", c.flash.rect, area)
		}

	})
}

func TestFlashGestureLanding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := flashCore(t)
		c.swipeBegin(ports.SwipeBegin{Fingers: 3})
		c.swipeUpdate(ports.SwipeUpdate{DX: -800, Time: 50 * time.Millisecond})
		c.swipeEnd(ports.SwipeEnd{Time: 60 * time.Millisecond})
		if c.flash.pending == nil || c.flash.pending.level != flashColumn {
			t.Fatalf("landing request %+v", c.flash.pending)
		}
		c.stopFlash()
		c.swipeBegin(ports.SwipeBegin{Fingers: 3, Time: 100 * time.Millisecond})
		c.swipeUpdate(ports.SwipeUpdate{DX: 800, Time: 150 * time.Millisecond})
		c.swipeEnd(ports.SwipeEnd{Cancelled: true, Time: 160 * time.Millisecond})
		if c.flash.pending != nil {
			t.Fatal("cancelled gesture flashed")
		}

	})
}

func TestFlashLockClearsPendingAndRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, running := range []bool{false, true} {
			c, clock := flashCore(t)
			state := ports.SecurityState{}
			gate := portsmocks.NewMockSessionSecurity(t)
			gate.EXPECT().Snapshot().RunAndReturn(func() ports.SecurityState { return state })
			c.opts.Security = gate
			if running {
				startFlash(t, c, clock, flashWindow, 3)
			} else {
				c.requestFlash(flashWindow, 3)
			}
			state = ports.SecurityState{Generation: 1, Protected: true}
			if !c.syncSecurity() {
				t.Fatal("security did not change")
			}
			if c.flash.pending != nil || c.flash.running != nil || c.flash.timerC != nil {
				t.Fatal("lock retained flash")
			}
			state = ports.SecurityState{Generation: 2}
			c.syncSecurity()
			if indicatorScene(t, c).Flash != nil || c.flash.pending != nil {
				t.Fatal("unlock requested flash")
			}
		}

	})
}

func TestFlashOverviewClickAndKeyboard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, click := range []bool{true, false} {
			c, _ := flashCore(t)
			c.cur().mon.ToggleOverview()
			scene := indicatorScene(t, c)
			if click {
				var rect Rect
				for _, p := range scene.Windows {
					if p.ID == 2 {
						rect = p.Rect
					}
				}
				c.cursorX, c.cursorY = float64(rect.X+rect.W/2), float64(rect.Y+rect.H/2)
				picked, err := c.overviewClick(context.Background())
				if err != nil || !picked {
					t.Fatalf("click %v %v", picked, err)
				}
			} else {
				if err := c.keyEvent(context.Background(), ports.KeyEvent{Keysym: "Return", Pressed: true}); err != nil {
					t.Fatal(err)
				}
			}
			if c.flash.pending == nil || c.flash.pending.level != flashWindow {
				t.Fatalf("overview request %+v", c.flash.pending)
			}
		}

	})
}

func TestFlashTileDrop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := flashCore(t)
		scene := indicatorScene(t, c)
		var rect Rect
		for _, p := range scene.Windows {
			if p.ID == 2 {
				rect = p.Rect
			}
		}
		c.cursorX, c.cursorY = float64(rect.X+rect.W/2), float64(rect.Y+rect.H/2)
		c.drag = &dragState{id: 3}
		if err := c.endDrag(context.Background()); err != nil {
			t.Fatal(err)
		}
		if c.flash.pending == nil || c.flash.pending.level != flashWindow || c.flash.pending.window != 3 {
			t.Fatalf("drop request %+v", c.flash.pending)
		}

	})
}

func TestFlashNoImplicitFocusTriggers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := flashCore(t)
		ctx := context.Background()
		indicatorScene(t, c)
		if c.flash.pending != nil {
			t.Fatal("startup publication flashed")
		}
		for _, ev := range []ports.ClientEvent{ports.WindowMapped{ID: 4}, ports.WindowActivate{ID: 1}} {
			if _, stop := c.clientEvent(ctx, ev); stop {
				t.Fatalf("event %T stopped", ev)
			}
			if c.flash.pending != nil {
				t.Fatalf("event %T flashed", ev)
			}
		}
		indicatorScene(t, c)
		if err := c.pointerMotion(ctx, ports.PointerMotion{X: 50, Y: 50}); err != nil {
			t.Fatal(err)
		}
		if c.flash.pending != nil {
			t.Fatal("hover focus flashed")
		}

	})
}

func TestFlashCancelsStaleRunningTargets(t *testing.T) {
	for _, level := range []flashLevel{flashWindow, flashColumn} {
		for _, change := range []string{"overview", "output gone", "fullscreen", "unmapped", "hidden", "other output", "workspace changed"} {
			t.Run(string(rune('0'+level))+"/"+change, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					c, clock := flashCore(t)
					a := c.cur()
					c.addScreen(ports.OutputInfo{Name: "B", Width: 300, Height: 200})
					b := c.screens[1]
					startFlash(t, c, clock, level, 3)
					if indicatorScene(t, c).Flash == nil {
						t.Fatal("setup: no running flash")
					}
					switch change {
					case "overview":
						a.mon.ToggleOverview()
					case "output gone":
						c.screens = []*screen{b}
						c.focusScreen = 0
					case "fullscreen":
						a.mon.ToggleFullscreen()
					case "unmapped":
						c.windows.drop(3)
					case "hidden":
						w := a.mon.Current()
						w.ToggleWindowStash()
						w.ToggleStashVisible()
					case "other output":
						a.mon.RemoveWindow(3)
						b.mon.AddWindow(3)
					case "workspace changed":
						a.mon.Focus(a.mon.Active + 1)
					}
					// Cancellation must not wait for the target output's next flip.
					scenes := stepScenes(t, c, b)
					if scenes["A"].Flash != nil || c.flash.running != nil || c.animating() {
						t.Fatalf("stale flash retained: %+v, animating=%v", scenes["A"].Flash, c.animating())
					}

				})
			})
		}
	}
}

func TestFlashCooldownRemembersEveryTarget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock := flashCore(t)
		startFlash(t, c, clock, flashWindow, 3)
		first := c.flash.start
		startFlash(t, c, clock, flashWindow, 2)
		c.requestFlash(flashWindow, 3)
		clock.now = clock.now.Add(flashSettle)
		if c.flashTick() || c.flash.running != nil {
			t.Fatal("A flashed again after B within cooldown")
		}
		clock.now = first.Add(flashCooldown)
		startFlash(t, c, clock, flashWindow, 3)
		if c.flash.running.window != 3 {
			t.Fatal("A did not flash after cooldown")
		}
		// Inserting after all previous starts expire prunes old target keys.
		clock.now = clock.now.Add(flashCooldown)
		startFlash(t, c, clock, flashWindow, 3)
		if len(c.flash.lastStarts) != 1 {
			t.Fatalf("expired cooldown entries retained: %v", c.flash.lastStarts)
		}

	})
}

// BenchmarkFlashSceneBuild includes revalidation and immutable scene creation.
func BenchmarkFlashSceneBuild(b *testing.B) {
	var cfg ports.Config
	cfg.Layout.MaxColumns = 2
	cfg.Keyboard.CmdKey = "super"
	cfg.Animations.On = true
	cfg.Focus.Flash = true
	cfg.Focus.FlashOpacity = 0.8
	cfg.Focus.FlashDuration = time.Hour
	c, err := New(cfg, Channels{Scenes: make(chan []ports.Scene, 1)}, Options{})
	if err != nil {
		b.Fatal(err)
	}
	c.addScreen(ports.OutputInfo{Name: "A", Width: 1000, Height: 600})
	c.windows.mapped(ports.WindowMapped{ID: 1}, time.Now())
	c.cur().mon.AddWindow(1)
	c.flash.running = &flashTarget{level: flashWindow, window: 1, output: "A"}
	c.flash.start = time.Now()
	c.flash.rect = Rect{W: 1000, H: 600}
	c.refreshShown()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		c.refreshShown()
		c.advanceFlash(nil)
		scene, err := c.sceneFor(ctx, 0, c.cur(), nil, nil)
		if err != nil {
			b.Fatal(err)
		}
		c.cur().last = scene
	}
}

func TestFlashExplicitActionOnFreshlyMappedWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock := flashCore(t)
		c.clientEvent(context.Background(), ports.WindowMapped{ID: 4})
		if c.flash.pending != nil {
			t.Fatal("mapping requested flash")
		}
		c.applyAction(ActionToggleFloating)
		if c.flash.pending == nil || c.flash.pending.window != 4 {
			t.Fatal("explicit action did not request flash")
		}
		c.stopAnimations()
		clock.now = clock.now.Add(flashSettle)
		if !c.flashTick() {
			t.Fatal("freshly mapped target rejected")
		}

	})
}

func TestFlashPendingTimerStopped(t *testing.T) {
	for _, reason := range []string{"replacement", "security", "run exit"} {
		t.Run(reason, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, clock := flashCore(t)
				timer := portsmocks.NewMockTimer(t)
				timer.EXPECT().C().Return(make(chan time.Time)).Once()
				timer.EXPECT().Stop().Return(true).Times(1)
				strictClock := portsmocks.NewMockClock(t)
				strictClock.EXPECT().Now().Return(clock.now).Maybe()
				strictClock.EXPECT().NewTimer(flashSettle).Return(timer).Once()
				c.opts.Clock = strictClock
				c.requestFlash(flashWindow, 3)
				// Only the pending flash timer belongs to the strict clock.
				c.opts.Clock = nil
				switch reason {
				case "replacement":
					c.requestFlash(flashWindow, 2)
					c.stopFlash()
				case "security":
					gate := portsmocks.NewMockSessionSecurity(t)
					gate.EXPECT().Snapshot().Return(ports.SecurityState{Generation: 1, Protected: true})
					c.opts.Security = gate
					c.syncSecurity()
				case "run exit":
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					if err := c.Run(ctx); err != nil {
						t.Fatal(err)
					}
				}

			})
		})
	}
}

func TestFlashUnchangedSampleIsReused(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock := flashCore(t)
		startFlash(t, c, clock, flashWindow, 3)
		first := indicatorScene(t, c).Flash
		if first == nil {
			t.Fatal("missing flash")
		}
		if next := indicatorScene(t, c).Flash; next != first {
			t.Fatal("unchanged immutable sample not reused")
		}
		clock.now = clock.now.Add(time.Millisecond)
		if next := indicatorScene(t, c).Flash; next == first || first.Alpha != c.cfg.Focus.FlashOpacity {
			t.Fatal("changed sample mutated prior scene")
		}

	})
}

func TestFlashCrossScreenActionsMarkDestinationWindow(t *testing.T) {
	for _, tc := range []struct {
		name     string
		action   Action
		occupied bool
		want     WindowID
	}{
		{"column focus to empty", ActionFocusColumnRight, false, 0},
		{"column focus to occupied", ActionFocusColumnRight, true, 4},
		{"monitor focus to empty", ActionFocusMonitorRight, false, 0},
		{"column move to empty", ActionMoveColumnRight, false, 3},
		{"column move to occupied", ActionMoveColumnRight, true, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, clock := flashCore(t)
				a := c.cur()
				c.addScreen(ports.OutputInfo{Name: "B", Width: 400, Height: 300})
				b := c.screens[1]
				if tc.occupied {
					c.windows.mapped(ports.WindowMapped{ID: 4}, clock.now)
					b.mon.AddWindow(4)
				}
				c.applyAction(tc.action)
				if c.cur() != b {
					t.Fatal("action did not cross screens")
				}
				id, _ := b.mon.Focused()
				if id != tc.want {
					t.Fatalf("focus %d want %d", id, tc.want)
				}
				if tc.want == 0 {
					if c.flash.pending != nil {
						t.Fatalf("empty destination request %+v", c.flash.pending)
					}
					clock.now = clock.now.Add(flashSettle)
					if c.flashTick() || stepScenes(t, c, b)["B"].Flash != nil {
						t.Fatal("empty destination flashed")
					}
					return
				}
				if c.flash.pending == nil || *c.flash.pending != (flashTarget{level: flashWindow, window: id, output: "B"}) {
					t.Fatalf("destination request %+v", c.flash.pending)
				}
				clock.now = clock.now.Add(flashSettle)
				if !c.flashTick() {
					t.Fatal("destination flash did not start")
				}
				scenes := stepScenes(t, c, b)
				if scenes["A"].Flash != nil {
					t.Fatal("source output flashed")
				}
				f := scenes["B"].Flash
				want, _, ok := c.flashRect(*c.flash.running, b)
				if f == nil || !ok || f.Rect != want || !scenes["B"].Animating {
					t.Fatalf("destination scene flash %+v", f)
				}
				if !tc.occupied && tc.action != ActionMoveColumnRight && len(scenes["B"].Windows) != 0 {
					t.Fatal("setup: destination is not empty")
				}
				clock.now = clock.now.Add(c.cfg.Focus.FlashDuration)
				scenes = stepScenes(t, c, b)
				if scenes["B"].Flash != nil || c.flashing(a) || c.animating() {
					t.Fatal("destination flash did not end")
				}

			})
		})
	}
}

func TestFlashSameScreenColumnFocusKeepsColumnLevel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock := flashCore(t)
		sc := c.cur()
		c.cur().mon.Current().FocusID(1)
		c.applyAction(ActionFocusColumnRight)
		if c.cur() != sc || c.flash.pending == nil || c.flash.pending.level != flashColumn {
			t.Fatalf("same-screen request %+v", c.flash.pending)
		}
		clock.now = clock.now.Add(flashSettle)
		if !c.flashTick() {
			t.Fatal("column flash did not start")
		}
		s := indicatorScene(t, c)
		for _, w := range s.Windows {
			if w.ID == c.flash.running.window && s.Flash.Rect != w.Rect {
				t.Fatalf("column rect %+v want %+v", s.Flash.Rect, w.Rect)
			}
		}

	})
}

func TestFlashCancelsOnGeometryChange(t *testing.T) {
	for _, level := range []flashLevel{flashWindow, flashColumn} {
		for _, change := range []string{"resize", "new window"} {
			t.Run(string(rune('0'+level))+"/"+change, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					c, clock := flashCore(t)
					startFlash(t, c, clock, level, 3)
					before := indicatorScene(t, c)
					if before.Flash == nil {
						t.Fatal("setup: no flash")
					}
					if change == "resize" {
						c.cur().mon.Current().Apply(ActionCycleColumnWidth)
					} else {
						c.cur().mon.AddWindow(4)
						c.windows.mapped(ports.WindowMapped{ID: 4}, clock.now)
					}
					after := indicatorScene(t, c)
					if after.Flash != nil || c.flash.running != nil {
						t.Fatal("stale geometry retained")
					}
					if before.Flash.Rect != c.flash.published.Rect {
						t.Fatal("old flash sample mutated")
					}

				})
			})
		}
	}
}

func TestFlashGeometryRevalidationAllocations(t *testing.T) {
	for _, level := range []flashLevel{flashWindow, flashColumn} {
		t.Run(string(rune('0'+level)), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, clock := flashCore(t)
				startFlash(t, c, clock, level, 3)
				// Measure production work, not the generated clock mock's call log.
				c.opts.Clock = nil
				c.flash.start = time.Now()
				c.cfg.Focus.FlashDuration = time.Hour
				c.advanceFlash(nil) // warm owner-owned layout scratch
				if n := testing.AllocsPerRun(100, func() { c.advanceFlash(nil) }); n != 0 {
					t.Fatalf("flash geometry validation allocates %v times per frame", n)
				}

			})
		})
	}
}

func TestFlashFollowsInnerBorderAndRadius(t *testing.T) {
	for _, test := range []struct {
		name   string
		gaps   int
		float  bool
		level  flashLevel
		radius int
	}{
		{"tile with gaps", 12, false, flashWindow, 11},
		{"column with gaps", 12, false, flashColumn, 11},
		{"tile without gaps", 0, false, flashWindow, 0},
		{"float", 0, true, flashWindow, 11},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, clock := flashCore(t)
				c.cfg.Border.Width, c.cfg.Border.Radius = 3, 14
				w := c.cur().mon.Current()
				w.Gaps = test.gaps
				if test.float {
					c.applyAction(ActionToggleFloating)
					c.stopFlash()
					c.stopAnimations()
				}
				id := WindowID(3)
				if f, ok := w.Focused(); ok && test.float {
					id = f
				}
				startFlash(t, c, clock, test.level, id)
				want := Rect{W: 300, H: 200}
				for _, p := range c.cur().mon.Layout() {
					if p.ID == id {
						want = p.Rect.Inset(p.Inset, 3)
						if test.level == flashColumn {
							want = c.flash.rect
						}
					}
				}

				if c.flash.rect != want || c.flash.radius != test.radius {
					t.Fatalf("rect %+v radius %d, want %+v radius %d", c.flash.rect, c.flash.radius, want, test.radius)
				}
				if f := c.flashFor(c.cur()); f == nil || f.Radius != test.radius {
					t.Fatalf("published %+v", f)
				}
				if test.level == flashWindow && test.gaps > 0 {
					for _, p := range c.cur().mon.Layout() {
						if p.ID == id && (p.Inset != ports.SideAll || c.flash.rect.W != p.Rect.W-6 || c.flash.rect.H != p.Rect.H-6) {
							t.Fatalf("tile %+v not inset by 3 on all sides: %+v", p, c.flash.rect)
						}
					}
				}

			})
		})
	}
}

func TestFlashClickFocus(t *testing.T) {
	for _, test := range []struct {
		name    string
		target  WindowID
		enabled bool
		want    bool
	}{
		{"different window", 1, true, true},
		{"already focused", 3, true, false},
		{"disabled", 1, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, clock := flashCore(t)
				c.cfg.Focus.Flash = test.enabled
				indicatorScene(t, c)
				if c.keyboardFocus() != 3 {
					t.Fatal("setup: expected window 3 focused")
				}
				c.pointer = test.target
				if err := c.pointerButton(context.Background(), ports.PointerButton{Button: btnLeft, Pressed: true}); err != nil {
					t.Fatal(err)
				}
				if c.keyboardFocus() != test.target {
					t.Fatalf("focus = %d, want %d", c.keyboardFocus(), test.target)
				}
				if (c.flash.pending != nil) != test.want {
					t.Fatalf("flash request = %+v, want flash %v", c.flash.pending, test.want)
				}
				// Let the click's viewport transition settle before the flash.
				clock.now = clock.now.Add(time.Second)
				stepScenes(t, c, c.cur())
				if c.flashTick() != test.want {
					t.Fatalf("flash start: want %v", test.want)
				}
				if test.want && (c.flash.running.window != test.target || c.flash.running.level != flashWindow || c.flashFor(c.cur()) == nil) {
					t.Fatalf("wrong running flash: %+v", c.flash.running)
				}

			})
		})
	}
}

func TestFlashClickModalFocus(t *testing.T) {
	for _, unchanged := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed", true: "unchanged"}[unchanged], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, _ := flashCore(t)
				w := c.cur().mon.Current()
				c.windows.mapped(ports.WindowMapped{ID: 4}, c.now())
				w.AddDialog(4, 1, 100, 80)
				w.SetModal(4, true)
				if !unchanged {
					w.Click(2)
				}
				indicatorScene(t, c)
				c.pointer = 1
				if err := c.pointerButton(context.Background(), ports.PointerButton{Button: btnLeft, Pressed: true}); err != nil {
					t.Fatal(err)
				}
				if c.keyboardFocus() != 4 {
					t.Fatalf("focus = %d, want modal 4", c.keyboardFocus())
				}
				if unchanged {
					if c.flash.pending != nil {
						t.Fatalf("unchanged modal flashed: %+v", c.flash.pending)
					}
				} else if c.flash.pending == nil || c.flash.pending.window != 4 {
					t.Fatalf("want modal flash, got %+v", c.flash.pending)
				}

			})
		})
	}
}

func TestFlashModifierClickDrag(t *testing.T) {
	for _, resize := range []bool{false, true} {
		for _, mode := range []string{"changed", "unchanged", "disabled"} {
			t.Run(map[bool]string{false: "move", true: "resize"}[resize]+"/"+mode, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					c, clock := flashCore(t)
					w := c.cur().mon.Current()
					w.Click(1)
					if resize {
						w.Apply(ActionToggleFloating)
					}
					if mode != "unchanged" {
						w.Click(3)
					}
					c.cfg.Focus.Flash = mode != "disabled"
					indicatorScene(t, c)
					c.pointer = 1
					c.mods = c.cmdMod
					button := uint32(btnLeft)
					if resize {
						button = btnRight
					}
					if err := c.pointerButton(context.Background(), ports.PointerButton{Button: button, Pressed: true}); err != nil {
						t.Fatal(err)
					}
					if c.drag == nil || c.drag.id != 1 || c.drag.resize != resize {
						t.Fatalf("drag not started: %+v", c.drag)
					}
					want := mode == "changed"
					if (c.flash.pending != nil) != want {
						t.Fatalf("flash = %+v, want %v", c.flash.pending, want)
					}
					clock.now = clock.now.Add(time.Second)
					stepScenes(t, c, c.cur())
					if c.flashTick() != want {
						t.Fatalf("flash start: want %v", want)
					}

				})
			})
		}
	}
}
