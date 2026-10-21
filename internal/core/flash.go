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
	radius     int // Corner radius of the target, as SceneWindow.Radius.
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
	rect, radius, ok := c.flashRect(target, sc)
	if !ok {
		return false
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
	f.running, f.rect, f.radius, f.start = &target, rect, radius, now
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
	if rect, radius, ok := c.flashRect(*f.running, sc); !ok || rect != f.rect || radius != f.radius {
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

// flashRect resolves the current target, never retaining a border after its
// visibility or geometry changes. stopFlash leaves the published sample intact
// so the scene diff also damages the old border.
func (c *Core) flashRect(target flashTarget, sc *screen) (Rect, int, bool) {
	if sc.mon.ov.open {
		return Rect{}, 0, false
	}
	if target.level == flashMonitor {
		o := sc.mon.Output()
		return Rect{W: o.W, H: o.H}, 0, true
	}
	owner, _ := c.screenOf(target.window)
	if owner != sc || c.windows.lookup(target.window).client.ID == 0 {
		return Rect{}, 0, false
	}
	view := sc.mon.viewport()
	c.flash.layout = sc.mon.layoutInto(c.flash.layout)
	var rect Rect
	radius := 0
	found := false
	for _, p := range c.flash.layout {
		if p.ID == target.window {
			if p.Fullscreen || p.Preview != 0 || !view.shows(p) {
				return Rect{}, 0, false
			}
			// The flash sits inside the drawn border, hugging the body.
			rect, found = c.clientRect(p), true
			radius = max(windowRadius(p, sc.mon.Current().gap(), c.cfg.Border.Radius)-c.cfg.Border.Width, 0)
			break
		}
	}
	if !found {
		return Rect{}, 0, false
	}
	if target.level == flashColumn {
		w := sc.mon.Current()
		col := w.columnOf(target.window)
		if col < 0 {
			return Rect{}, 0, false
		}
		for _, p := range c.flash.layout {
			if !p.Hidden && slices.Contains(w.Columns[col].Windows, p.ID) {
				rect = union(rect, c.clientRect(p))
			}
		}
	}
	return rect, radius, true
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
	sample := ports.FocusFlash{Rect: f.rect, Width: c.cfg.Focus.FlashWidth, Radius: f.radius, Color: color, Alpha: f.value}
	if f.published == nil || *f.published != sample {
		// Allocate only for a changed sample; sample itself stays on the stack.
		published := new(ports.FocusFlash)
		*published = sample
		f.published = published
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
