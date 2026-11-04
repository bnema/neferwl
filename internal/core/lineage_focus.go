package core

import "time"

const lineageTick = 10 * time.Millisecond

type focusMoment struct {
	at time.Time
	id WindowID
}

// focusHistory is owner-only, bounded and embedded in its output. since keeps
// the start of the current uninterrupted focus even after ring wraparound.
type focusHistory struct {
	entries     [16]focusMoment
	next, count int
	current     WindowID
	since       time.Time
}

func (h *focusHistory) observe(at time.Time, id WindowID) {
	if h.count != 0 && id == h.current {
		return
	}
	h.current, h.since = id, at
	h.entries[h.next] = focusMoment{at, id}
	h.next = (h.next + 1) % len(h.entries)
	if h.count < len(h.entries) {
		h.count++
	}
}

func (h *focusHistory) focusedAt(id WindowID, at time.Time) bool {
	if at.IsZero() || h.count == 0 {
		return false
	}
	if h.current == id && !at.Add(lineageTick).Before(h.since) {
		return true
	}
	oldest := (h.next - h.count + len(h.entries)) % len(h.entries)
	if at.Add(lineageTick).Before(h.entries[oldest].at) {
		return false
	}
	// A tick-quantized process start may lie either side of a boundary.
	for n := 0; n < h.count; n++ {
		entry := h.entries[(oldest+n)%len(h.entries)]
		if entry.id != id || at.Add(lineageTick).Before(entry.at) {
			continue
		}
		if n == h.count-1 || !at.Add(-lineageTick).After(h.entries[(oldest+n+1)%len(h.entries)].at) {
			return true
		}
	}
	return false
}

func (c *Core) branchFocused(launcher WindowID, at time.Time) bool {
	sc, _ := c.screenOf(launcher)
	return sc != nil && sc.lineageFocus.focusedAt(launcher, at)
}

func (c *Core) observeDomainFocus(sc *screen, id WindowID) {
	h := &sc.lineageFocus
	if h.count == 0 || h.current != id {
		h.observe(c.now(), id)
	}
}
