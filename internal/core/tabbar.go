package core

import "github.com/bnema/neferwl/internal/domain"

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

