package core

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"

	portsmocks "github.com/bnema/neferwl/internal/mocks/ports"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestPositionHintChangesAndTiming(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock := indicatorCore(t)
		c.cfg.Focus.PositionHint, c.cfg.Animations.On = true, true
		a := c.cur()
		a.mon.Current().Overflow = OverflowCascade
		a.mon.Current().MaxColumns = 1
		a.mon.AddWindow(1)
		a.mon.AddWindow(2)
		c.addScreen(domain.OutputInfo{Name: "B", Width: 300, Height: 200})
		b := c.screens[1]
		a.mon.Current().Overflow, a.mon.Current().MaxColumns = OverflowCascade, 1
		a.mon.Current().Focus = 1
		c.updatePositionHints(nil)
		require.False(t, a.positionHint.live)
		a.mon.Current().Focus = 0
		c.updatePositionHints(nil)
		require.True(t, a.positionHint.sample.Shown())
		require.False(t, b.positionHint.live)
		start := a.positionHint.start
		clock.now = clock.now.Add(100 * time.Millisecond)
		c.updatePositionHints(nil)
		require.Equal(t, start, a.positionHint.start, "no-op restarted timer")
		a.mon.Current().Focus = 1
		c.updatePositionHints(nil)
		require.Equal(t, clock.now, a.positionHint.start)
		clock.now = clock.now.Add(positionHintHold)
		c.updatePositionHints(nil)
		require.True(t, c.positionHintFading(a), "hold boundary must start frame scheduling")
		clock.now = clock.now.Add(positionHintFade / 2)
		c.updatePositionHints(nil)
		require.Equal(t, float32(0.5), a.positionHint.sample.Opacity)
		saved := a.positionHint.sample
		c.focusScreen = 1
		c.updatePositionHints(b)
		require.Equal(t, saved, a.positionHint.sample)
		require.True(t, b.positionHint.live)
		clock.now = clock.now.Add(positionHintFade / 2)
		c.updatePositionHints(a)
		require.False(t, a.positionHint.live)
		require.True(t, b.positionHint.live)

	})
}

func TestPositionHintWorkspaceAndSuppression(t *testing.T) {
	for _, mode := range []string{"overview", "fullscreen", "off", "no-animation"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, clock := indicatorCore(t)
				c.cfg.Focus.PositionHint = true
				sc := c.cur()
				sc.mon.AddWindow(1)
				c.updatePositionHints(nil)
				sc.mon.show(sc.mon.Workspaces[1])
				c.updatePositionHints(nil)
				require.True(t, sc.positionHint.live)
				switch mode {
				case "overview":
					sc.mon.ov.open = true
				case "fullscreen":
					sc.mon.Current().Overflow = OverflowFixed
					sc.mon.Current().Fullscreen = 1
				case "off":
					c.cfg.Focus.PositionHint = false
				default:
					clock.now = clock.now.Add(positionHintHold)
				}
				c.updatePositionHints(nil)
				require.False(t, sc.positionHint.sample.Shown())
				require.Nil(t, c.positionHintTimer.timerC)

			})
		})
	}
}

func TestPositionHintGrid(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := indicatorCore(t)
		m := c.cur().mon
		w := m.Current()
		w.Overflow, w.MaxColumns = OverflowCascade, 1
		m.AddWindow(1)
		m.AddWindow(2)
		h := positionHintGrid(m)
		require.False(t, h.Vertical)
		require.Equal(t, uint8(2), h.Counts[0])
		require.Equal(t, uint8(1), h.Counts[1], "empty noncascade workspace")
		require.Equal(t, uint8(1), h.ActiveRow)
		m.show(m.Workspaces[1])
		h = positionHintGrid(m)
		require.True(t, h.Vertical, "current workspace decides axis")
		require.Equal(t, uint8(1), h.Columns)
		require.Equal(t, uint8(2), h.Counts[0])
		require.Equal(t, uint8(1), h.ActiveRow)

	})
}

func TestPositionHintAllocations(t *testing.T) {
	// Real clock avoids measuring generated mock bookkeeping, as publishRig does.
	c := publishRig(t, 1)
	c.cfg.Focus.PositionHint = true
	sc := c.cur()
	w := sc.mon.Current()
	w.Overflow, w.MaxColumns = OverflowCascade, 1
	c.updatePositionHints(nil)

	for name, run := range map[string]func(){
		"trigger": func() { w.Focus = (w.Focus + 1) % 2; c.updatePositionHints(nil) },
		"fade": func() {
			sc.positionHint.live = true
			sc.positionHint.start = c.now().Add(-positionHintHold - 50*time.Millisecond)
			c.updatePositionHints(nil)
		},
		"inactive": func() { sc.positionHint.live = false; c.updatePositionHints(nil) },
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				defer c.stopPositionHintTimer()
				run()
				require.Zero(t, testing.AllocsPerRun(100, run))
			})
		})
	}
}

func TestPositionHintMoveWorkspaceDestination(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := indicatorCore(t)
		c.cfg.Focus.PositionHint = true
		c.cur().mon.AddWindow(1)
		c.addScreen(domain.OutputInfo{Name: "B", Width: 300, Height: 200})
		c.updatePositionHints(nil)
		source := c.cur()
		moved := source.mon.Current()
		c.moveWorkspace(dirRight)
		c.updatePositionHints(nil)
		require.Same(t, moved, c.cur().mon.Current())
		require.False(t, source.positionHint.live)
		require.True(t, c.cur().positionHint.sample.Shown())

	})
}

func TestPositionHintCapsAndProtection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := indicatorCore(t)
		c.cfg.Focus.PositionHint = true
		m := c.cur().mon
		for range 20 {
			m.Workspaces = append(m.Workspaces, m.newWorkspace())
		}
		m.Active = 20
		h := positionHintGrid(m)
		require.Equal(t, uint8(16), h.Counts[0])
		require.Equal(t, uint8(15), h.ActiveRow)
		m.Current().Overflow = OverflowCascade
		h = positionHintGrid(m)
		require.Equal(t, uint8(16), h.Columns)
		require.Equal(t, uint8(15), h.ActiveCol)
		c.updatePositionHints(nil)
		c.cur().positionHint.live = true
		c.security.Protected = true
		c.updatePositionHints(nil)
		require.False(t, c.cur().positionHint.live)

	})
}

func TestPositionHintIncludesNamedEmptyAndMixedOverflow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := indicatorCore(t)
		m := c.cur().mon
		w := m.Current()
		w.Overflow, w.MaxColumns = OverflowCascade, 1
		m.AddWindow(1)
		m.AddWindow(2)
		named := m.newWorkspace()
		named.Name, named.Overflow = "hidden", OverflowScroll
		named.Columns = []Column{{Windows: []WindowID{3}}, {Windows: []WindowID{4}}}
		named.SetBandStart(1, true)
		m.hidden = append(m.hidden, named)
		empty := m.newWorkspace()
		empty.Name = "empty"
		m.hidden = append(m.hidden, empty)
		h := positionHintGrid(m)
		require.Equal(t, uint8(4), h.Columns)
		require.Equal(t, [16]uint8{2, 1, 1, 1}, h.Counts)
		require.Equal(t, uint8(0), h.ActiveCol)

	})
}

func TestPositionHintOverviewCommittedBaseline(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "accept", true: "cancel"}[cancel], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, _ := indicatorCore(t)
				c.cfg.Focus.PositionHint = true
				m := c.cur().mon
				m.AddWindow(1)
				origin := m.Current()
				other := m.Workspaces[1]
				other.AddWindow(2)
				require.False(t, indicatorScene(t, c).PositionHint.Shown())
				m.ToggleOverview()
				require.False(t, indicatorScene(t, c).PositionHint.Shown())
				m.showOverview(other)
				require.False(t, indicatorScene(t, c).PositionHint.Shown())
				require.Same(t, origin, c.cur().positionHint.workspace)
				m.showOverview(origin)
				require.False(t, indicatorScene(t, c).PositionHint.Shown())
				m.showOverview(other)
				require.False(t, indicatorScene(t, c).PositionHint.Shown())
				if cancel {
					m.CancelOverview()
				} else {
					m.ToggleOverview()
				}
				require.Equal(t, !cancel, indicatorScene(t, c).PositionHint.Shown())

			})
		})
	}
}

func TestPositionHintScrolledAwayFullscreen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := indicatorCore(t)
		c.cfg.Focus.PositionHint = true
		c.addScreen(domain.OutputInfo{Name: "B", Width: 300, Height: 200})
		a := c.screens[0]
		w := a.mon.Current()
		w.Overflow = OverflowScroll
		a.mon.AddWindow(1)
		a.mon.AddWindow(2)
		w.Fullscreen = 1
		w.View = modelColumnX(w, 1)
		require.NotZero(t, w.Fullscreen)
		require.Zero(t, w.Cover())
		c.focusScreen = 1
		c.updatePositionHints(nil)
		c.focusScreen = 0
		c.updatePositionHints(nil)
		require.True(t, a.positionHint.sample.Shown())

	})
}

func TestPositionHintStartupHotplugReload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := indicatorCore(t)
		c.cfg.Focus.PositionHint = true
		require.False(t, indicatorScene(t, c).PositionHint.Shown())
		c.addScreen(domain.OutputInfo{Name: "B", Width: 300, Height: 200})
		for _, s := range stepScenes(t, c, nil) {
			require.False(t, s.PositionHint.Shown())
		}
		require.NoError(t, c.apply(c.cfg))
		for _, s := range stepScenes(t, c, nil) {
			require.False(t, s.PositionHint.Shown())
		}
		c.focusScreen = 1
		require.True(t, stepScenes(t, c, nil)["B"].PositionHint.Shown())

	})
}

func TestPositionHintMaxGridTransitionAllocations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := publishRig(t, 1)
		c.cfg.Focus.PositionHint = true
		m := c.cur().mon
		for len(m.Workspaces) < 16 {
			m.Workspaces = append(m.Workspaces, m.newWorkspace())
		}
		for _, w := range m.Workspaces {
			w.Overflow, w.MaxColumns = OverflowCascade, 1
			w.Columns = make([]Column, 16)
			for i := range w.Columns {
				w.Columns[i] = Column{Windows: []WindowID{WindowID(i + 100)}}
				w.SetBandStart(i, true)
			}
		}
		c.updatePositionHints(nil)
		defer c.stopPositionHintTimer()
		run := func() { m.Active = (m.Active + 1) % 16; c.updatePositionHints(nil) }
		run()
		require.Equal(t, [16]uint8{16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16}, c.cur().positionHint.sample.Counts)
		require.Zero(t, testing.AllocsPerRun(100, run))

	})
}

func TestPositionHintRunTimerLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := publishRig(t, 2)
		c.cfg.Focus.PositionHint, c.cfg.Animations.On = true, true
		c.ch.Commands = nil
		now := time.Unix(1000, 0)
		clock := portsmocks.NewMockClock(t)
		clock.EXPECT().Now().RunAndReturn(func() time.Time { return now }).Maybe()
		timer := portsmocks.NewMockTimer(t)
		tick := make(chan time.Time, 1)
		timer.EXPECT().C().Return((<-chan time.Time)(tick)).Maybe()
		timer.EXPECT().Reset(500 * time.Millisecond).Return(true).Once()
		timer.EXPECT().Reset(100 * time.Millisecond).Return(true).Once()
		timer.EXPECT().Stop().Return(true).Once()
		timer.EXPECT().Reset(positionHintHold).Return(true).Once()
		clock.EXPECT().NewTimer(positionHintHold).Return(timer).Once()
		// The second timer is the existing animation fallback, not another hint timer.
		frame := portsmocks.NewMockTimer(t)
		frame.EXPECT().C().Return(make(chan time.Time)).Maybe()
		frame.EXPECT().Stop().Return(true).Maybe()
		frame.EXPECT().Reset(mock.Anything).Return(true).Maybe()
		clock.EXPECT().NewTimer(mock.Anything).Return(frame).Maybe()
		c.opts.Clock = clock
		a, b := c.screens[0], c.screens[1]
		a.mon.Current().Overflow, a.mon.Current().MaxColumns = OverflowCascade, 1
		a.mon.Current().Focus = 1
		c.updatePositionHints(nil)
		a.mon.Current().Focus = 0
		c.updatePositionHints(nil)
		now = now.Add(100 * time.Millisecond)
		c.focusScreen = 1
		c.updatePositionHints(nil)
		require.Same(t, timer, c.positionHintTimer.timer)
		frames := make(chan domain.OutputFrame)
		c.ch.Frames = frames
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- c.Run(ctx) }()
		synctest.Wait()
		now = now.Add(500 * time.Millisecond)
		tick <- now
		synctest.Wait()
		require.True(t, a.positionHint.fading)
		require.False(t, b.positionHint.fading)
		// B still holds for 100ms; the same timer selects its deadline.
		now = now.Add(100 * time.Millisecond)
		tick <- now
		synctest.Wait()
		require.True(t, b.positionHint.fading)
		require.Nil(t, c.positionHintTimer.timerC)
		now = now.Add(positionHintFade)
		frames <- domain.OutputFrame{Output: "A"}
		synctest.Wait()
		frames <- domain.OutputFrame{Output: "B"}
		synctest.Wait()
		require.False(t, a.positionHint.sample.Shown())
		require.False(t, b.positionHint.sample.Shown())
		require.False(t, c.animating())
		require.Nil(t, c.positionHintTimer.timerC)
		// Owner is durably blocked: disabling a newly restarted hint stops
		// the retained timer instead of leaving an idle wakeup armed.
		c.focusScreen = 0
		c.updatePositionHints(nil)
		require.NotNil(t, c.positionHintTimer.timerC)
		c.cfg.Focus.PositionHint = false
		c.updatePositionHints(nil)
		require.Nil(t, c.positionHintTimer.timerC)
		cancel()
		synctest.Wait()
		require.NoError(t, <-done)
	})
}
