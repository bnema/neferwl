package core

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bnema/neferwl/internal/domain"
	"github.com/bnema/neferwl/internal/ports"
)

func TestReturnToLauncher(t *testing.T) {
	for _, animate := range []bool{false, true} {
		for _, remote := range []bool{false, true} {
			for _, scenario := range []string{"focused", "not focused", "launcher gone", "other workspace", "middle column"} {
				t.Run(fmt.Sprintf("animations=%v/remote=%v/%s", animate, remote, scenario), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						c, ic, _ := fadeCore(t)
						c.cfg.Animations.On = animate
						desktop := c.cur()
						c.clientEvent(context.Background(), ports.WindowMapped{ID: 9, AppID: "kitty"})
						var target *screen
						if remote {
							c.addScreen(domain.OutputInfo{Name: "tv", Virtual: true, Width: 640, Height: 360})
							target = c.screens[c.screenIndex("tv")]
							c.cfg.Outputs = []domain.OutputConfig{{Name: "tv", KeepFocus: true}}
							c.rules = []rule{{appID: regexp.MustCompile("^picture$"), output: "tv"}}
						} else {
							target = desktop
							desktop.mon.RemoveWindow(9)
						}
						c.clientEvent(context.Background(), ports.WindowMapped{ID: 1, AppID: "picture", Proc: lineageNodes(10), Xwayland: true, Fullscreen: true})
						c.clientEvent(context.Background(), ports.WindowMapped{ID: 2, AppID: "game", Proc: lineageNodes(20, 10), Xwayland: true})
						w := target.mon.Current()
						if scenario == "middle column" || scenario == "not focused" || scenario == "launcher gone" || scenario == "other workspace" {
							// An unrelated middle tile is the normal neighbour fallback.
							w.AddTile(3, 0, false)
							c.windows.mapped(ports.WindowMapped{ID: 3}, c.now())
							// Ensure order P, middle, G irrespective of insertion policy.
							gi, mi := w.ColumnOf(2), w.ColumnOf(3)
							if mi > gi {
								w.Columns[gi], w.Columns[mi] = w.Columns[mi], w.Columns[gi]
							}
							w.FocusID(2)
						}
						switch scenario {
						case "not focused":
							w.FocusID(3)
						case "launcher gone":
							c.clientEvent(context.Background(), ports.WindowUnmapped{ID: 1})
						case "other workspace":
							w.RemoveWindow(1)
							target.mon.Workspaces[1].AddWindow(1)
						}
						indicatorScene(t, c)
						seat, keyboard, pointer := c.focusScreen, c.keyboardFocus(), c.pointer
						c.clientEvent(context.Background(), ports.WindowUnmapped{ID: 2})
						ic.now = ic.now.Add(10 * time.Second)
						c.animate(ic.now, nil)
						indicatorScene(t, c)
						want := WindowID(1)
						if scenario == "not focused" || scenario == "launcher gone" || scenario == "other workspace" {
							want = 3
						}
						if f, _ := w.Focused(); f != want {
							t.Fatalf("focus %d want %d", f, want)
						}
						if remote && (c.focusScreen != seat || c.keyboardFocus() != keyboard || c.pointer != pointer) {
							t.Fatal("remote close changed seat")
						}
						if want == 1 {
							if w.Fullscreen != 1 {
								t.Fatal("launcher fullscreen lost")
							}
							var cover Placement
							for _, p := range w.Layout() {
								if p.ID == 1 {
									cover = p
								}
							}
							if !cover.Fullscreen || cover.Rect != w.Output {
								t.Fatalf("launcher layout: %+v", cover)
							}
							if c.x11.sent != 1 {
								t.Fatalf("X11 active %d want launcher; shown=%+v viewport=%+v", c.x11.sent, target.shownLayout(), target.mon.viewport())
							}
							// The settled scene must retain the same fullscreen geometry.
							found := false
							for _, p := range target.settledLayout {
								if p.ID == 1 {
									found = true
									if !p.Fullscreen || p.Rect != w.Output {
										t.Fatalf("scene placement: %+v", p)
									}
								}
							}
							if !found {
								t.Fatal("launcher absent from scene")
							}
						}
					})
				})
			}
		}
	}
}
