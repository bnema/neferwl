package core

import (
	"time"

	"github.com/bnema/neferwl/internal/core/layout"
	"github.com/bnema/neferwl/internal/ports"
)

const positionHintHold = 600 * time.Millisecond
const positionHintFade = 150 * time.Millisecond

// positionHintState belongs exclusively to the core owner. Published samples
// are fixed-size values, so no buffer recycling or renderer acknowledgement is
// needed. The timer is retained (but stopped when idle) and Reset on reuse.
type positionHintState struct {
	workspace   *Workspace
	band        int
	focused     bool
	initialized bool
	start       time.Time
	live        bool
	fading      bool
	sample      ports.PositionHint
}

type positionHintTimer struct {
	timer  ports.Timer
	timerC <-chan time.Time
}

// updatePositionHints compares actual positions at the publication boundary,
// independent of which input/action changed them. Initial output discovery is
// not a move. Other outputs retain their own start time and sample.
func (c *Core) updatePositionHints(only *screen) {
	for i, sc := range c.screens {
		h := &sc.positionHint
		// Overview selection is provisional. Preserve the last committed
		// baseline until closing, so accept compares against entry and cancel
		// cannot announce intermediate browsing positions.
		if sc.mon.ov.open {
			h.live, h.sample = false, ports.PositionHint{}
			continue
		}
		// Disabled hints skip the band walk; dropping the baseline keeps a
		// later re-enable from announcing the position it finds.
		if !c.cfg.Focus.PositionHint {
			*h = positionHintState{}
			continue
		}
		w := sc.mon.Current()
		band := w.LaneOf(w.Focus)
		focused := i == c.focusScreen
		changed := h.initialized && (h.workspace != w || h.band != band || focused && !h.focused)
		// Transferring the current workspace changes the source's fallback
		// position too, but only the receiving output should announce it.
		if h.initialized && h.workspace != w {
			for _, other := range c.screens {
				if other != sc && other.mon.has(h.workspace) {
					changed = false
					break
				}
			}
		}
		h.workspace, h.band, h.focused, h.initialized = w, band, focused, true
		if c.protectionRequested() || w.cover() != 0 {
			h.live, h.sample = false, ports.PositionHint{}
			continue
		}
		if changed {
			h.start, h.live, h.fading = c.now(), true, false
		}
		if !h.live {
			continue
		}
		if !changed && only != nil && only != sc {
			continue
		}
		elapsed := c.now().Sub(h.start)
		opacity := float32(1)
		h.fading = elapsed >= positionHintHold
		if h.fading {
			if !c.animOn() || elapsed >= positionHintHold+positionHintFade {
				h.live, h.sample = false, ports.PositionHint{}
				continue
			}
			opacity = 1 - float32(elapsed-positionHintHold)/float32(positionHintFade)
		}
		h.sample = positionHintGrid(sc.mon)
		h.sample.Opacity = opacity
	}
	c.armPositionHintTimer()
}

// Use ext-workspace's complete ordered list: numbered (including empty
// spares), then configured hidden named workspaces. Unlike overview cards,
// these remain addressable even when empty. Window the list at its active
// entry when it exceeds the cap. Bands stay top-aligned; excess rows clamp.
func positionHintGrid(m *Monitor) ports.PositionHint {
	h := ports.PositionHint{Vertical: m.overviewAxis() == layout.Vertical}
	active, total := 0, 0
	for w := range m.all() {
		if w == m.Current() {
			active = total
		}
		total++
	}
	cap := ports.PositionHintMaxColumns
	if h.Vertical {
		cap = ports.PositionHintMaxRows
	}
	first := max(0, active-cap+1)
	if h.Vertical {
		h.Columns, h.Counts[0], h.ActiveRow = 1, uint8(min(total, cap)), uint8(active-first)
		return h
	}
	h.Columns, h.ActiveCol = uint8(min(total, cap)), uint8(active-first)
	i := 0
	for w := range m.all() {
		if i >= first && i < first+cap {
			n := max(1, w.Engine().LaneCount(w.Snapshot()))
			content, _ := w.Engine().Axes()
			if content.Span(w.Usable) <= 0 {
				n = 1
			}
			h.Counts[i-first] = uint8(min(n, ports.PositionHintMaxRows))
		}
		i++
	}
	w := m.Current()
	h.ActiveRow = uint8(min(w.LaneOf(w.Focus), ports.PositionHintMaxRows-1))
	return h
}

// positionHintFading drives fade frames through the shared scene publication,
// like springs and the focus flash: each fade frame republishes the whole
// scene, which allocates its slices. The hint state itself allocates nothing.
func (c *Core) positionHintFading(sc *screen) bool {
	return sc.positionHint.live && sc.positionHint.fading
}

// One timer wakes at the earliest hold boundary. Fade frames use the existing
// flip/fallback scheduler; with no live hints no timer is armed or clock read.
func (c *Core) armPositionHintTimer() {
	var earliest time.Time
	for _, sc := range c.screens {
		if sc.positionHint.live && !sc.positionHint.fading {
			end := sc.positionHint.start.Add(positionHintHold)
			if earliest.IsZero() || end.Before(earliest) {
				earliest = end
			}
		}
	}
	t := &c.positionHintTimer
	if earliest.IsZero() {
		c.stopPositionHintTimer()
		return
	}
	d := max(earliest.Sub(c.now()), time.Nanosecond)
	if t.timer == nil {
		t.timer = newPortTimer(c.opts.Clock, d)
	} else {
		t.timer.Reset(d)
	}
	t.timerC = t.timer.C()
}

func (c *Core) stopPositionHintTimer() {
	t := &c.positionHintTimer
	if t.timerC != nil {
		t.timer.Stop()
		t.timerC = nil
	}
}
