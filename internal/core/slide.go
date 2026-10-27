package core

import (
	"math"

	"github.com/bnema/neferwl/internal/core/motion"
)

// Slides move what is on screen smoothly: a workspace's columns (view), its
// stash (stashView) and the monitor between workspaces (switchView). All
// are pure presentation over the settled state (View, Focus, stashAt,
// Active): the layout ends where the state says as soon as a slide stops.

// switchSpring is the workspace slide's spring from off: it snaps once it
// is under half a logical pixel of the output span along the switch axis
// from its target (slideLayout rounds the offset times that span).
func (m *Monitor) switchSpring(off, velocity float64) motion.Spring {
	s := motion.WorkspaceSpring(off, velocity)
	if h := m.switchAxis.Span(m.template.Output); h > 0 {
		s.Snap = motion.PixelSnap / float64(h)
	}
	return s
}

// stopSwitch lands the workspace slide and forgets the list it measured in.
func (m *Monitor) stopSwitch() { m.switchView.Stop(); m.switchList = nil }

// framedSwitch keeps transitions involving a sized workspace settled. A
// monitor-wide animation has no per-workspace crop; it must not expose
// columns outside their viewport while sliding between different frames.
func (m *Monitor) framedSwitch() bool {
	if m.Current().Output != m.Output() {
		return true
	}
	list := m.Workspaces
	if m.switchList != nil {
		list = m.switchList
	}
	base := indexOf(list, m.Current())
	if base < 0 {
		return false
	}
	pos := float64(base) + m.switchView.Off
	for _, i := range [2]int{int(math.Floor(pos)), int(math.Ceil(pos))} {
		if i >= 0 && i < len(list) && m.has(list[i]) && list[i].Output != m.Output() {
			return true
		}
	}
	return false
}

// slideLayout adds the workspaces a workspace slide shows next to the
// current one, moved by their distance, to the current layout (moved too).
// A landing slide places them as the list was when the swipe began
// (switchList): an empty workspace dropped since shows as background.
func (m *Monitor) slideLayout(cur []Placement) []Placement {
	if m.switchView.Off == 0 || m.shown != nil || m.framedSwitch() {
		return cur
	}
	list := m.Workspaces
	if m.switchList != nil {
		list = m.switchList
	}
	base := indexOf(list, m.Current())
	if base < 0 {
		return cur
	}
	h := float64(m.switchAxis.Span(m.template.Output))
	pos := float64(base) + m.switchView.Off
	offset := func(p []Placement, k int) []Placement {
		d := int(math.Round((float64(k) - pos) * h))
		for i := range p {
			if !p[i].Hidden {
				m.switchAxis.Offset(&p[i].Rect, d)
			}
		}
		return p
	}
	cur = offset(cur, base)
	for _, k := range []int{int(math.Floor(pos)), int(math.Ceil(pos))} {
		if k == base || k < 0 || k >= len(list) || !m.has(list[k]) {
			continue
		}
		next := list[k].AppendLayout(m.slideBuf[:0])
		m.slideBuf = next
		for i := range next {
			next[i].Focused = false
		}
		// Windows of the neighbor are already listed hidden: replace them.
		if m.slideShown == nil {
			m.slideShown = map[WindowID]Placement{}
		}
		shown := m.slideShown
		clear(shown)
		for _, p := range offset(next, k) {
			shown[p.ID] = p
		}
		for i, p := range cur {
			if v, ok := shown[p.ID]; ok {
				cur[i] = v
			}
		}
	}
	return cur
}
