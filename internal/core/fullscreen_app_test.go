package core

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/bnema/neferwl/internal/ports"
)

func TestFullscreenAppArrival(t *testing.T) {
	for _, mode := range []Overflow{OverflowCascade, OverflowScroll, OverflowFixed} {
		for _, floating := range []bool{false, true} {
			for _, tc := range []struct {
				name        string
				first, next ports.WindowMapped
				same        bool
			}{
				{"pid", ports.WindowMapped{PID: 7, AppID: "launcher"}, ports.WindowMapped{PID: 7, AppID: "game"}, true},
				{"app-id", ports.WindowMapped{PID: 7, AppID: "app"}, ports.WindowMapped{PID: 8, AppID: "app"}, true},
				{"different", ports.WindowMapped{PID: 7, AppID: "launcher"}, ports.WindowMapped{PID: 8, AppID: "game"}, false},
				{"empty", ports.WindowMapped{}, ports.WindowMapped{}, false},
				{"pid-empty-app-id", ports.WindowMapped{PID: 7}, ports.WindowMapped{PID: 7}, true},
				{"empty-app-id", ports.WindowMapped{PID: 7}, ports.WindowMapped{PID: 8}, false},
			} {
				t.Run(fmt.Sprintf("%s/float=%v/%s", mode, floating, tc.name), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						c, _, _ := fadeCore(t)
						w := c.cur().mon.Current()
						w.SetOverflow(mode)
						tc.first.ID, tc.next.ID = 1, 2
						tc.first.Floating = floating
						c.clientEvent(context.Background(), tc.first)
						c.clientEvent(context.Background(), ports.WindowFullscreenRequest{ID: 1, Fullscreen: true})
						before := w.View
						c.clientEvent(context.Background(), tc.next)
						if w.Fullscreen != 1 {
							t.Fatal("fullscreen lost")
						}
						want := WindowID(1)
						if tc.same {
							want = 2
						}
						if id, _ := w.Focused(); id != want {
							t.Fatalf("focus %d, want %d", id, want)
						}
						for _, p := range w.Layout() {
							if p.ID == 2 && (p.Hidden == tc.same || tc.same && !p.Rect.Overlaps(w.Output)) {
								t.Fatalf("arrival placement: %+v", p)
							}
						}
						if !tc.same && w.View != before {
							t.Fatal("other app moved view")
						}
						if tc.same {
							switch mode {
							case OverflowFixed:
								if !w.IsFloat(2) || w.Cover() != 1 {
									t.Fatal("fixed arrival not above cover")
								}
							case OverflowCascade:
								if w.Band(w.ColumnOf(2)) != w.Band(w.ColumnOf(1))+1 || w.View == before {
									t.Fatal("cascade did not select new band")
								}
							case OverflowScroll:
								if w.ColumnOf(2) != w.ColumnOf(1)+1 || w.View == before {
									t.Fatal("scroll did not select right neighbor")
								}
							}
						}
					})
				})
			}
		}
	}
}
