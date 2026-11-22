package core

import (
	"context"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

// tabBarScratch holds the bars a scene build computes before it knows
// whether they changed. Owner goroutine only; never published.
type tabBarScratch struct {
	bars   []domain.TabBar
	titles []domain.TabTitle
}

// appendTabBars appends to the scratch the bars of w's tabbed columns whose
// visible tab layout draws on v, not fullscreen: the bar sits right above
// the tab, as wide. off moves the bars (capture scenes are frame-local).
func (c *Core) appendTabBars(s *tabBarScratch, w *Workspace, layout []Placement, v viewport, off Rect) {
	if w.TabBar <= 0 {
		return
	}
	for i := range w.Columns {
		col := &w.Columns[i]
		if !col.Tabbed {
			continue
		}
		id := col.Selected()
		k := placementIndex(layout, id)
		if k < 0 {
			continue
		}
		p := layout[k]
		if p.Fullscreen || !v.shows(p) {
			continue
		}
		bar := Rect{X: p.Rect.X - off.X, Y: p.Rect.Y - w.TabInset() - off.Y, W: p.Rect.W, H: w.TabBar}
		first := len(s.titles)
		n := len(col.Windows)
		tw := bar.W / n
		for j, tab := range col.Windows {
			r := Rect{X: bar.X + j*tw, Y: bar.Y, W: tw, H: bar.H}
			if j == n-1 {
				r.W = bar.X + bar.W - r.X
			}
			var title string
			if rec := c.windows.records[tab]; rec != nil {
				title = rec.client.Title
			}
			s.titles = append(s.titles, domain.TabTitle{Rect: r, Title: title, Active: j == col.Focus, Window: tab})
		}
		s.bars = append(s.bars, domain.TabBar{Rect: bar, Radius: windowRadius(p, w.Gap(), c.cfg.Border.Radius)})
		s.bars[len(s.bars)-1].Tabs = s.titles[first:len(s.titles):len(s.titles)]
	}
}

// publishTabBars returns the bars to publish: prev when the scratch draws
// the same (an unchanged scene allocates nothing), else an immutable copy
// in two allocations.
func publishTabBars(s *tabBarScratch, prev []domain.TabBar) []domain.TabBar {
	if len(s.bars) == 0 {
		return nil
	}
	if domain.SameTabBars(s.bars, prev) {
		return prev
	}
	bars := append([]domain.TabBar(nil), s.bars...)
	titles := append([]domain.TabTitle(nil), s.titles...)
	at := 0
	for i := range bars {
		n := len(bars[i].Tabs)
		bars[i].Tabs = titles[at : at+n : at+n]
		at += n
	}
	return bars
}

func placementIndex(layout []Placement, id WindowID) int {
	for k := range layout {
		if layout[k].ID == id {
			return k
		}
	}
	return -1
}


// tabAt is the tab of sc's published bars at lx, ly (output-local
// logical); over is set on a bar even between tabs.
func tabAt(sc *screen, lx, ly float64) (id WindowID, over bool) {
	in := func(r Rect) bool {
		return lx >= float64(r.X) && lx < float64(r.X+r.W) && ly >= float64(r.Y) && ly < float64(r.Y+r.H)
	}
	for i := range sc.last.TabBars {
		bar := &sc.last.TabBars[i]
		if !in(bar.Rect) {
			continue
		}
		for _, t := range bar.Tabs {
			if in(t.Rect) {
				return t.Window, true
			}
		}
		return 0, true
	}
	return 0, false
}

// tabClick handles a press on a tab bar of the screen under the pointer:
// a left press shows and focuses the tab, with cmd held it starts that
// tab's tile drag; other buttons do nothing. No client sees the press or
// its release. handled is false off the bars.
func (c *Core) tabClick(ctx context.Context, v ports.PointerButton) (handled bool, err error) {
	if !v.Pressed || c.pointer != 0 || c.grab != 0 || c.held() != 0 {
		return false, nil
	}
	i := c.pointerScreenAt(c.cursorX, c.cursorY, v.Device)
	if i < 0 {
		return false, nil
	}
	sc := c.screens[i]
	if sc.mon.previewing() {
		return false, nil
	}
	id, over := tabAt(sc, c.cursorX-float64(sc.x), c.cursorY-float64(sc.y))
	if !over {
		return false, nil
	}
	w := sc.mon.Current()
	if v.Button != btnLeft || id == 0 || w.ColumnOf(id) < 0 {
		c.buttons[v.Button], c.swallow[v.Button] = true, true
		return true, nil
	}
	now := c.now()
	before := c.snapshot(now)
	c.focusClick(sc, w, id, false)
	c.transition(before, now)
	if !c.synthetic && c.cmdMod != 0 && c.mods&c.cmdMod != 0 && !c.keyboard.inhibited(c.keyboardFocus()) {
		// The drag owns the button: its release ends the drag.
		return true, c.beginDrag(ctx, id, v.Button, false, 0)
	}
	c.buttons[v.Button], c.swallow[v.Button] = true, true
	return true, c.publish(ctx)
}
