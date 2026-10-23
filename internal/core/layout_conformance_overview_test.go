package core

import "testing"

// Overview invariants: one tile per window, none duplicated, visible tiles
// inside the span, exactly one focused tile inside the selection, and lane
// projection that agrees with laneOf.
func TestLayoutConformanceOverview(t *testing.T) {
	forEachConformance(t, func(t *testing.T, l layout, w *Workspace, k, n int, mode string) {
		v := w.lview()
		tiles, span, sel := l.previewTiles(v, nil, nil)
		if len(tiles) != 2*n {
			t.Fatalf("%d tiles for %d windows", len(tiles), 2*n)
		}
		seen := map[WindowID]bool{}
		focused := 0
		for _, tile := range tiles {
			if seen[tile.ID] {
				t.Fatalf("duplicate tile %d", tile.ID)
			}
			seen[tile.ID] = true
			if tile.Hidden {
				continue
			}
			if tile.Rect.X < 0 || tile.Rect.X+tile.Rect.W > span {
				t.Fatalf("tile outside span: %+v, span %d", tile, span)
			}
			if tile.Focused {
				focused++
				if tile.Rect.X < sel.X || tile.Rect.Y < sel.Y || tile.Rect.X+tile.Rect.W > sel.X+sel.W || tile.Rect.Y+tile.Rect.H > sel.Y+sel.H {
					t.Fatal("focused tile outside selection")
				}
			}
		}
		if n == 0 {
			return
		}
		if span <= 0 || sel.X < 0 || sel.X+sel.W > span || focused != 1 {
			t.Fatalf("invalid selection %+v, span %d, focused %d", sel, span, focused)
		}
		if l.laneCount(v) > 1 && !l.previewLanes() {
			t.Fatal("multiple lanes without lane projection")
		}
		if l.previewLanes() {
			for i, c := range v.Columns {
				for _, id := range c.Windows {
					for _, tile := range tiles {
						if tile.ID == id && !tile.Hidden && tile.Rect.Y/v.Usable.H != l.laneOf(v, i) {
							t.Fatal("tile lane differs from laneOf")
						}
					}
				}
			}
		}
	})
}
