package core

import (
	"slices"
	"time"

	"github.com/bnema/neferwl/internal/ports"
)

type flashLevel uint8

const (
	flashWindow flashLevel = iota
	flashColumn
	flashMonitor
	flashSettle   = 150 * time.Millisecond
	flashRecheck  = 50 * time.Millisecond
	flashCooldown = time.Second
)

type flashTarget struct {
	level  flashLevel
	window WindowID
	output string
}

type focusFlash struct {
	pending    *flashTarget
	since      time.Time
	timerC     <-chan time.Time
	timerStop  func() bool
	running    *flashTarget
	rect       Rect
	start      time.Time
	value      float64 // Retained between this output's own flips.
	lastStarts map[flashTarget]time.Time
	layout     []Placement       // Owner scratch, never shared with a published scene.
	published  *ports.FocusFlash // Immutable last sample, reused when unchanged.
}

func (c *Core) flashOn() bool { return c.animOn() && c.cfg.Focus.Flash && !c.protectionRequested() }

func (c *Core) requestFlash(level flashLevel, window WindowID) {
	if !c.flashOn() {
		return
	}
	c.stopFlash()
	f := &c.flash
	f.pending = &flashTarget{level: level, window: window, output: c.cur().name()}
	f.since = c.now()
	f.timerC, f.timerStop = newTimer(c.opts.Clock, flashSettle)
}

func (c *Core) flashTick() bool {
	f := &c.flash
	f.timerC, f.timerStop = nil, nil
	if f.pending == nil {
		return false
	}
	target := *f.pending
	f.pending = nil
	if !c.flashOn() {
		return false
	}
	i := c.screenIndex(target.output)
	if i < 0 {
		return false
	}
	sc := c.screens[i]
	if sc.mon.ov.open {
		return false
	}
	now := c.now()
	if target.level != flashMonitor {
		owner, _ := c.screenOf(target.window)
		record := c.windows.lookup(target.window)
		if owner != sc || record.client.ID == 0 {
			return false
		}
	}
	wait := flashSettle - now.Sub(f.since)
	if wait > 0 || !sc.settled() {
		if wait <= 0 {
			wait = flashRecheck
		}
		f.pending = &target
		f.timerC, f.timerStop = newTimer(c.opts.Clock, wait)
		return false
	}
	layout := sc.mon.Layout()
	var rect Rect
	if target.level == flashMonitor {
		o := sc.mon.Output()
		rect = Rect{W: o.W, H: o.H}
	} else {
		at := slices.IndexFunc(layout, func(p Placement) bool { return p.ID == target.window && !p.Hidden && p.Preview == 0 })
		if at < 0 || layout[at].Fullscreen {
			return false
		}
		rect = layout[at].Rect
		if target.level == flashColumn {
			w := sc.mon.Current()
			col := w.columnOf(target.window)
			if col < 0 {
				return false
			}
			for _, p := range layout {
				if p.Hidden || !slices.Contains(w.Columns[col].Windows, p.ID) {
					continue
				}
				rect = union(rect, p.Rect)
			}
		}
	}
	if at, ok := f.lastStarts[target]; ok && now.Sub(at) < flashCooldown {
		return false
	}
	if f.lastStarts == nil {
		f.lastStarts = make(map[flashTarget]time.Time)
	}
	for key, at := range f.lastStarts {
		if now.Sub(at) >= flashCooldown {
			delete(f.lastStarts, key)
		}
	}
	f.lastStarts[target] = now
	f.running, f.rect, f.start = &target, rect, now
	f.value = c.cfg.Focus.FlashOpacity
	return true
}

func (c *Core) advanceFlash(only *screen) {
	f := &c.flash
	if f.running == nil {
		return
	}
	if !c.flashOn() || c.screenIndex(f.running.output) < 0 {
		c.stopFlash()
		return
	}
	sc := c.screens[c.screenIndex(f.running.output)]
	if !c.flashVisible(*f.running, sc) {
		c.stopFlash()
		return
	}
	if only != nil && only.name() != f.running.output {
		return
	}
	d := c.cfg.Focus.FlashDuration
	elapsed := c.now().Sub(f.start)
	if d <= 0 || elapsed >= d {
		f.running = nil
		f.value = 0
		return
	}
	f.value = c.cfg.Focus.FlashOpacity * (1 - float64(max(elapsed, 0))/float64(d))
}

// flashVisible checks the current layout, not leaving placements retained for
// transitions: an old border must not outlive its target's visibility.
func (c *Core) flashVisible(target flashTarget, sc *screen) bool {
	if sc.mon.ov.open {
		return false
	}
	if target.level == flashMonitor {
		return true
	}
	owner, _ := c.screenOf(target.window)
	if owner != sc || c.windows.lookup(target.window).client.ID == 0 {
		return false
	}
	view := sc.mon.viewport()
	c.flash.layout = sc.mon.layoutInto(c.flash.layout)
	for _, p := range c.flash.layout {
		if p.ID == target.window {
			return !p.Fullscreen && p.Preview == 0 && view.shows(p)
		}
	}
	return false
}

func (c *Core) flashFor(sc *screen) *ports.FocusFlash {
	f := &c.flash
	if !c.flashing(sc) {
		return nil
	}
	color := c.cfg.Focus.FlashColor
	if color == "" {
		color = c.cfg.Border.Active
	}
	sample := ports.FocusFlash{Rect: f.rect, Width: c.cfg.Focus.FlashWidth, Color: color, Alpha: f.value}
	if f.published == nil || *f.published != sample {
		f.published = &ports.FocusFlash{Rect: sample.Rect, Width: sample.Width, Color: sample.Color, Alpha: sample.Alpha}
	}
	return f.published
}

func (c *Core) flashing(sc *screen) bool {
	return c.flash.running != nil && c.flash.running.output == sc.name()
}

func (c *Core) stopFlashTimer() {
	if c.flash.timerStop != nil {
		c.flash.timerStop()
	}
	c.flash.timerC, c.flash.timerStop = nil, nil
}

func (c *Core) stopFlash() {
	c.stopFlashTimer()
	c.flash.pending, c.flash.running, c.flash.value = nil, nil, 0
}

func (m *Monitor) settled() bool {
	if m.switchView.busy() {
		return false
	}
	for w := range m.all() {
		if w.view.busy() || w.stashView.busy() {
			return false
		}
	}
	return true
}

func (s *screen) settled() bool { return s.mon.settled() && len(s.rects) == 0 }
